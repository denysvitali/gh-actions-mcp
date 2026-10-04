package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/denysvitali/gh-actions-mcp/github"
)

// observeMonitor owns only this bounded call; all continuation state is signed
// into the cursor so recovery and process restarts need no background waiter.
func (s *MCPServer) observeMonitor(ctx context.Context, client *github.Client, t monitorTarget, seconds int, delta bool) (monitorResult, error) { //nolint:gocognit,gocyclo // Bounded polling preserves the latest snapshot on deadline.
	budget := time.Duration(seconds) * time.Second
	requestBudget := budget
	if requestBudget <= 0 {
		requestBudget = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, requestBudget)
	defer cancel()
	start := time.Now()
	var latest monitorResult
	for {
		snapshot, err := monitorSnapshot(ctx, client, t)
		if err != nil {
			if ctx.Err() != nil && latest.SHA != "" {
				latest.StopReason = "budget_exhausted"
				return s.finishMonitor(latest, t, delta)
			}
			return latest, err
		}
		latest = snapshot
		changed := monitorChanged(snapshot, t.Previous)
		if latest.Complete || seconds == 0 || delta && changed || time.Since(start) >= budget {
			break
		}
		timer := time.NewTimer(min(5*time.Second, budget-time.Since(start)))
		select {
		case <-ctx.Done():
			timer.Stop()
			latest.StopReason = "budget_exhausted"
			return s.finishMonitor(latest, t, delta)
		case <-timer.C:
		}
	}
	if ctx.Err() != nil && !latest.Complete {
		latest.StopReason = "budget_exhausted"
	}
	latest.StopReason = monitorStopReason(latest, t.Previous, delta)
	s.monitorDiagnoses(ctx, client, t, &latest)
	return s.finishMonitor(latest, t, delta)
}

func monitorSnapshot(ctx context.Context, client *github.Client, t monitorTarget) (monitorResult, error) {
	out := monitorResult{Repository: t.Owner + "/" + t.Repo, SHA: t.SHA, ObservedAt: monitorNow(), Coverage: "observed_actions_only", MissingWorkflows: []string{}, Workflows: []monitorRun{}, Jobs: []monitorJob{}}
	if t.RunID > 0 {
		return monitorRunSnapshot(ctx, client, t, out)
	}
	checks, err := client.GetCheckRunsForRef(ctx, t.SHA, nil)
	if err != nil {
		return out, err
	}
	out.TotalWorkflows = checks.TotalCount
	out.State = checks.State
	out.Complete = len(checks.CheckRuns) > 0
	for _, run := range checks.CheckRuns {
		if run.Status != "completed" {
			out.Complete = false
		}
		if len(out.Workflows) < 100 {
			out.Workflows = append(out.Workflows, monitorRun{ID: run.ID, WorkflowID: run.WorkflowID, Attempt: run.RunAttempt, Name: shortMonitorName(run.Name), Status: run.Status, Conclusion: run.Conclusion})
		} else {
			out.Truncated = true
		}
	}
	if out.State == "pending" {
		out.Complete = false
	}
	monitorExpected(&out, t.Expected, checks.CheckRuns)
	monitorJobs(ctx, client, &out)
	return out, nil
}

func monitorRunSnapshot(ctx context.Context, client *github.Client, t monitorTarget, out monitorResult) (monitorResult, error) {
	run, err := client.GetWorkflowRun(ctx, t.RunID)
	if err != nil {
		return out, err
	}
	if run.HeadSHA != t.SHA {
		return out, fmt.Errorf("run SHA changed; verify target again")
	}
	out.Coverage = "pinned_run_attempt"
	out.ExpectedAttempt = t.Attempt
	out.TotalWorkflows = 1
	out.Workflows = append(out.Workflows, monitorRun{ID: run.ID, WorkflowID: run.WorkflowID, Attempt: run.RunAttempt, Name: shortMonitorName(run.Name), Status: run.Status, Conclusion: run.Conclusion})
	out.State = "pending"
	if run.RunAttempt > t.Attempt && t.Attempt > 0 {
		out.State = "superseded"
		out.Complete = true
		return out, nil
	}
	if run.RunAttempt < t.Attempt {
		out.StopReason = "awaiting_attempt"
		return out, nil
	}
	if run.Status == "completed" && run.Conclusion != "" {
		out.Complete = true
		out.State = run.Conclusion
	}
	monitorJobs(ctx, client, &out)
	return out, nil
}

func monitorExpected(out *monitorResult, expected []string, checks []*github.CheckRun) {
	if len(expected) == 0 {
		return
	}
	out.Coverage = "explicit_workflow_set"
	// If the snapshot was capped, do not claim that an omitted workflow is absent.
	if out.Truncated {
		out.Coverage = "incomplete_workflow_snapshot"
		out.Complete = false
	}
	for _, want := range expected {
		found := false
		for _, run := range checks {
			if run.Name == want || strconv.FormatInt(run.WorkflowID, 10) == want {
				found = true
				break
			}
		}
		if !found {
			out.MissingWorkflows = append(out.MissingWorkflows, want)
		}
	}
	if len(out.MissingWorkflows) > 0 {
		out.Complete = false
		out.State = "pending"
	}
}

func monitorJobs(ctx context.Context, client *github.Client, out *monitorResult) {
	inspected := 0
	for _, run := range out.Workflows {
		if run.Status == "completed" && run.Conclusion == "success" {
			continue
		}
		if inspected >= 10 {
			out.Truncated = true
			break
		}
		inspected++
		jobs, err := client.GetWorkflowJobs(ctx, run.ID, "", run.Attempt)
		if err != nil {
			out.Warnings = append(out.Warnings, fmt.Sprintf("jobs unavailable for run %d", run.ID))
			continue
		}
		for _, job := range jobs {
			if len(out.Jobs) >= 100 {
				out.Truncated = true
				break
			}
			out.Jobs = append(out.Jobs, monitorJob{ID: job.ID, RunID: run.ID, Attempt: run.Attempt, Name: shortMonitorName(job.Name), Status: job.Status, Conclusion: job.Conclusion})
		}
	}
	sort.Slice(out.Jobs, func(i, j int) bool { return out.Jobs[i].ID < out.Jobs[j].ID })
}

func shortMonitorName(s string) string {
	r := []rune(s)
	if len(r) > 128 {
		return string(r[:128]) + "…"
	}
	return s
}

func monitorHash(v any) string {
	data, _ := json.Marshal(v)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:12])
}

func monitorState(out monitorResult) map[string]string {
	state := map[string]string{"state": monitorHash([]any{out.State, out.Complete, out.Coverage, out.MissingWorkflows, out.Truncated})}
	for _, r := range out.Workflows {
		state[fmt.Sprintf("run:%d", r.ID)] = monitorHash(r)
	}
	for _, j := range out.Jobs {
		state[fmt.Sprintf("job:%d", j.ID)] = monitorHash(j)
	}
	return state
}

func monitorChanged(out monitorResult, previous map[string]string) bool {
	return monitorHash(monitorState(out)) != monitorHash(previous)
}

func (s *MCPServer) finishMonitor(out monitorResult, t monitorTarget, delta bool) (monitorResult, error) {
	current := monitorState(out)
	if delta {
		out = monitorDelta(out, t.Previous, current)
	}
	t.Previous = current
	var err error
	out.Cursor, err = s.encodeMonitorCursor(t)
	out.ChangedOnly = delta
	if !out.Complete {
		out.NextPollSeconds = 5
	}
	return out, err
}

func monitorDelta(out monitorResult, previous, current map[string]string) monitorResult {
	runs := make([]monitorRun, 0)
	for _, r := range out.Workflows {
		key := fmt.Sprintf("run:%d", r.ID)
		if previous[key] != current[key] {
			runs = append(runs, r)
		}
	}
	jobs := make([]monitorJob, 0)
	for _, j := range out.Jobs {
		key := fmt.Sprintf("job:%d", j.ID)
		if previous[key] != current[key] {
			jobs = append(jobs, j)
		}
	}
	for key := range previous {
		if _, ok := current[key]; !ok {
			out.Removed = append(out.Removed, key)
		}
	}
	sort.Strings(out.Removed)
	out.Workflows = runs
	out.Jobs = jobs
	return out
}

func (s *MCPServer) monitorDiagnoses(ctx context.Context, client *github.Client, t monitorTarget, out *monitorResult) {
	if !t.Diagnose || out.State == "superseded" || out.StopReason == "awaiting_attempt" {
		return
	}
	for _, run := range out.Workflows {
		if !shouldDiagnoseMonitorRun(t, run, out.Jobs) {
			continue
		}
		if len(out.Diagnoses) >= 2 {
			out.Warnings = append(out.Warnings, "Additional failed workflows omitted from diagnosis; use diagnose_failure with their run IDs.")
			break
		}
		diagnosis, err := client.DiagnoseFailure(ctx, run.ID, false, 30)
		if err != nil {
			out.Warnings = append(out.Warnings, fmt.Sprintf("diagnosis unavailable for run %d", run.ID))
			continue
		}
		if diagnosis.HeadSHA != out.SHA || diagnosis.RunAttempt != run.Attempt {
			out.Warnings = append(out.Warnings, fmt.Sprintf("run %d changed attempt during diagnosis; resume monitoring", run.ID))
			continue
		}
		out.Diagnoses = append(out.Diagnoses, diagnosis)
	}
}

func monitorHasFailure(run monitorRun, jobs []monitorJob) bool {
	if run.Conclusion == "failure" || run.Conclusion == "timed_out" {
		return true
	}
	for _, job := range jobs {
		if job.RunID == run.ID && (job.Conclusion == "failure" || job.Conclusion == "timed_out") {
			return true
		}
	}
	return false
}

func monitorFailureChanged(run monitorRun, jobs []monitorJob, previous map[string]string) bool {
	if previous[fmt.Sprintf("run:%d", run.ID)] != monitorHash(run) {
		return true
	}
	for _, job := range jobs {
		if job.RunID == run.ID && previous[fmt.Sprintf("job:%d", job.ID)] != monitorHash(job) {
			return true
		}
	}
	return false
}

func monitorStopReason(out monitorResult, previous map[string]string, delta bool) string {
	switch {
	case out.Complete:
		return "completed"
	case out.StopReason != "":
		return out.StopReason
	case !delta:
		return "snapshot"
	case monitorChanged(out, previous):
		return "changed"
	default:
		return "unchanged"
	}
}

func shouldDiagnoseMonitorRun(t monitorTarget, run monitorRun, jobs []monitorJob) bool {
	if t.RunID > 0 && t.Attempt != run.Attempt {
		return false
	}
	return monitorHasFailure(run, jobs) && monitorFailureChanged(run, jobs, t.Previous)
}
