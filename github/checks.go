package github

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/google/go-github/v89/github"
)

// CheckRun represents a GitHub check run
type CheckRun struct {
	ID          int64  `json:"id"`
	HeadSHA     string `json:"head_sha"`
	WorkflowID  int64  `json:"workflow_id"`
	RunAttempt  int    `json:"run_attempt"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	Conclusion  string `json:"conclusion,omitempty"`
	StartedAt   string `json:"started_at,omitempty"`
	CompletedAt string `json:"completed_at,omitempty"`
	AppName     string `json:"app_name,omitempty"`
	DetailsURL  string `json:"details_url,omitempty"`
}

// CombinedCheckStatus represents Actions workflow runs for one commit.
// It does not include third-party Checks API or commit status contexts.
type CombinedCheckStatus struct {
	SHA          string         `json:"sha"`
	State        string         `json:"state"` // "pending", "success", "failure", "neutral"
	TotalCount   int            `json:"total_count"`
	CheckRuns    []*CheckRun    `json:"check_runs"`
	ByConclusion map[string]int `json:"by_conclusion"`
}

// GetCheckRunsOptions contains parameters for getting check runs
type GetCheckRunsOptions struct {
	CheckName string // Optional: filter by check name
	Status    string // Optional: queued, in_progress, completed
	Filter    string // Optional: "latest" (default) or "all"
}

// isLikelyCommitRef reports whether ref looks like a (possibly abbreviated) commit
// SHA: 7 to 40 hex digits. Anything else is treated as a branch name. The
// Full SHAs can be used directly; shorter values are resolved through GitHub
// before being used as immutable workflow filters.
func isLikelyCommitRef(ref string) bool {
	if len(ref) < 7 || len(ref) > 40 {
		return false
	}
	for _, ch := range ref {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') && (ch < 'A' || ch > 'F') {
			return false
		}
	}
	return true
}

// checkRunFilter is the resolved form of GetCheckRunsOptions plus the ref
// classification, so the filtering pass needs no nil checks.
type checkRunFilter struct {
	ref         string
	refIsCommit bool
	name        string
	status      string
	// keepAll disables newest-run-per-workflow deduplication.
	keepAll bool
}

// newCheckRunFilter resolves opts (which may be nil) against ref.
func newCheckRunFilter(ref string, opts *GetCheckRunsOptions) checkRunFilter {
	filter := checkRunFilter{ref: ref, refIsCommit: isLikelyCommitRef(ref)}
	if opts != nil {
		filter.name = opts.CheckName
		filter.status = opts.Status
		filter.keepAll = opts.Filter == "all"
	}
	return filter
}

// matches enforces the exact resolved SHA even when a proxy ignores head_sha.
func (f checkRunFilter) matches(run *github.WorkflowRun) bool {
	if run == nil {
		return false
	}
	if f.refIsCommit && !strings.EqualFold(run.GetHeadSHA(), f.ref) {
		return false
	}
	if f.name != "" && run.GetName() != f.name {
		return false
	}
	if f.status != "" && run.GetStatus() != f.status {
		return false
	}
	return true
}

// ResolveRef resolves a branch, tag, or abbreviated SHA against the target repository.
// A full SHA is already immutable and needs no additional API permission.
// An omitted ref uses the local HEAD, preserving the existing default.
func (c *Client) ResolveRef(ctx context.Context, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		commit, err := GetLastCommit()
		if err != nil {
			return "", fmt.Errorf("failed to get current commit: %w", err)
		}
		ref = commit.SHA
	}
	if len(ref) == 40 && isLikelyCommitRef(ref) {
		return strings.ToLower(ref), nil
	}
	commit, _, err := c.gh.Repositories.GetCommit(ctx, c.owner, c.repo, ref, nil)
	if err != nil {
		return "", fmt.Errorf("failed to resolve ref %s: %w", ref, err)
	}
	sha := commit.GetSHA()
	if len(sha) != 40 || !isLikelyCommitRef(sha) {
		return "", fmt.Errorf("ref %s resolved to an invalid commit SHA", ref)
	}
	return strings.ToLower(sha), nil
}

// GetCheckRunsForRef reports Actions workflow state for one immutable commit.
// It deliberately does not claim coverage of third-party checks or required-check rules.
// All API pages are read; latest mode retains the newest run for each workflow ID.
func (c *Client) GetCheckRunsForRef(ctx context.Context, ref string, opts *GetCheckRunsOptions) (*CombinedCheckStatus, error) {
	sha, err := c.ResolveRef(ctx, ref)
	if err != nil {
		return nil, err
	}
	filter := newCheckRunFilter(sha, opts)
	runs, err := c.workflowRunsForSHA(ctx, sha)
	if err != nil {
		return nil, err
	}
	matched := make([]*github.WorkflowRun, 0, len(runs))
	for _, run := range runs {
		if filter.matches(run) {
			matched = append(matched, run)
		}
	}

	if !filter.keepAll {
		matched = latestRunPerName(matched)
	}
	return c.combinedStatus(sha, matched), nil
}

func (c *Client) workflowRunsForSHA(ctx context.Context, sha string) ([]*github.WorkflowRun, error) {
	runOpts := &github.ListWorkflowRunsOptions{HeadSHA: sha, ListOptions: github.ListOptions{PerPage: 100}}
	runsFound := make([]*github.WorkflowRun, 0)
	fetched := 0
	for {
		runs, response, err := c.gh.Actions.ListRepositoryWorkflowRuns(ctx, c.owner, c.repo, runOpts)
		if err != nil {
			return nil, fmt.Errorf("failed to list workflow runs for ref %s: %w", sha, err)
		}
		fetched += len(runs.WorkflowRuns)
		runsFound = append(runsFound, runs.WorkflowRuns...)
		if response == nil || response.NextPage == 0 {
			if fetched < runs.GetTotalCount() {
				return nil, fmt.Errorf("workflow run listing incomplete: received %d of %d", fetched, runs.GetTotalCount())
			}
			break
		}
		if response.NextPage <= runOpts.Page {
			return nil, fmt.Errorf("workflow run pagination did not advance")
		}
		runOpts.Page = response.NextPage
	}
	return runsFound, nil
}

// latestRunPerName retains its historical helper name, but uses stable workflow
// identity. The name fallback supports older proxies that omit workflow_id.
func latestRunPerName(runs []*github.WorkflowRun) []*github.WorkflowRun {
	latest := make(map[string]*github.WorkflowRun, len(runs))
	for _, run := range runs {
		key := fmt.Sprintf("id:%d", run.GetWorkflowID())
		if run.GetWorkflowID() == 0 {
			key = "name:" + run.GetName()
		}
		old := latest[key]
		if old == nil || run.GetRunNumber() > old.GetRunNumber() ||
			(run.GetRunNumber() == old.GetRunNumber() && (run.GetRunAttempt() > old.GetRunAttempt() ||
				(run.GetRunAttempt() == old.GetRunAttempt() && run.GetID() > old.GetID()))) {
			latest[key] = run
		}
	}
	result := make([]*github.WorkflowRun, 0, len(latest))
	for _, run := range latest {
		result = append(result, run)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].GetID() < result[j].GetID() })
	return result
}

// combinedStatus projects workflow runs onto the check-run shape and aggregates
// them. A run's conclusion is counted when it has one; otherwise its
// still-running status is counted instead, so pending work is visible in
// ByConclusion.
func (c *Client) combinedStatus(ref string, runs []*github.WorkflowRun) *CombinedCheckStatus {
	result := &CombinedCheckStatus{
		SHA:          ref,
		CheckRuns:    make([]*CheckRun, 0, len(runs)),
		ByConclusion: make(map[string]int),
	}

	for _, run := range runs {
		result.CheckRuns = append(result.CheckRuns, &CheckRun{
			ID:          run.GetID(),
			HeadSHA:     run.GetHeadSHA(),
			WorkflowID:  run.GetWorkflowID(),
			RunAttempt:  run.GetRunAttempt(),
			Name:        run.GetName(),
			Status:      run.GetStatus(),
			Conclusion:  run.GetConclusion(),
			StartedAt:   formatTime(run.RunStartedAt),
			CompletedAt: completedRunTime(run),
			AppName:     "github-actions",
			DetailsURL:  run.GetHTMLURL(),
		})

		switch {
		case run.GetConclusion() != "":
			result.ByConclusion[run.GetConclusion()]++
		case run.GetStatus() != "completed":
			result.ByConclusion[run.GetStatus()]++
		}
	}

	result.TotalCount = len(result.CheckRuns)
	result.State = c.determineOverallState(result.CheckRuns)
	return result
}

// determineOverallState aggregates individual check runs into one state.
// Precedence is pending > failure > success > neutral: any unfinished check makes
// the whole ref pending. Only skipped or neutral conclusions are neutral;
// cancellation and action-required states cannot turn another success green.
// An empty set is pending.
func (c *Client) determineOverallState(checkRuns []*CheckRun) string {
	if len(checkRuns) == 0 {
		return "pending"
	}

	hasPending := false
	hasFailure := false
	hasSuccess := false

	for _, cr := range checkRuns {
		if cr.Status == "completed" {
			switch cr.Conclusion {
			case "failure", "timed_out", "cancelled", "action_required", "startup_failure", "stale":
				hasFailure = true
			case "success":
				hasSuccess = true
			case "neutral", "skipped":
			default:
				hasPending = true
			}
		} else {
			// queued, in_progress, etc.
			hasPending = true
		}
	}

	if hasPending {
		return "pending"
	}
	if hasFailure {
		return "failure"
	}
	if hasSuccess {
		return "success"
	}
	return "neutral"
}

func completedRunTime(run *github.WorkflowRun) string {
	if run.GetStatus() != "completed" {
		return ""
	}
	return formatTime(run.UpdatedAt)
}
