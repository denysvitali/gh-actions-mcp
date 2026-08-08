package github

import (
	"context"
	"fmt"
	"time"
)

// WaitRunResult reports how a wait on a workflow run ended.
//
// Status is "completed", "timed_out" or "cancelled". TimeoutReached is true only
// for "timed_out". Conclusion is GitHub's run conclusion, except in fail-fast
// mode where it may be the conclusion of the first failing job or step.
type WaitRunResult struct {
	Status          string  `json:"status"`               // "completed", "timed_out"
	Conclusion      string  `json:"conclusion,omitempty"` // "success", "failure", etc.
	DurationSeconds float64 `json:"duration"`
	RunURL          string  `json:"run_url"`
	StartedAt       string  `json:"started_at,omitempty"`
	CompletedAt     string  `json:"completed_at,omitempty"`
	TimeoutReached  bool    `json:"timeout_reached"`
	PollCount       int     `json:"poll_count"`
}

// WaitCommitChecksResult reports how a wait on a commit's checks ended.
// OverallConclusion is the aggregate state ("success", "failure", "pending",
// "neutral") or "timed_out"/"cancelled" when the wait itself ended early.
type WaitCommitChecksResult struct {
	OverallConclusion  string         `json:"overall_conclusion"` // "success", "failure", "pending", "neutral"
	ChecksTotal        int            `json:"checks_total"`
	ChecksByConclusion map[string]int `json:"checks_by_conclusion"`
	DurationSeconds    float64        `json:"duration_seconds"`
	TimeoutReached     bool           `json:"timeout_reached"`
}

// ManageRunAction is an action ManageRun can perform on a workflow run.
type ManageRunAction string

const (
	ManageRunActionCancel      ManageRunAction = "cancel"
	ManageRunActionRerun       ManageRunAction = "rerun"
	ManageRunActionRerunFailed ManageRunAction = "rerun_failed"
)

// ManageRunResult reports the outcome of a ManageRun call. Status is "success"
// or "failed"; a failed API call is reported here rather than as an error.
type ManageRunResult struct {
	RunID   int64           `json:"run_id"`
	Action  ManageRunAction `json:"action"`
	Status  string          `json:"status"` // "success", "failed"
	Message string          `json:"message,omitempty"`
}

// WaitResult is the result of WaitForWorkflowRun. Run holds the last observed
// state, which may be nil if the very first poll was cancelled.
type WaitResult struct {
	Run       *WorkflowRun
	TimedOut  bool
	Elapsed   time.Duration
	PollCount int
}

// waitMode selects how waitForRun decides that a run is finished.
type waitMode int

const (
	// waitModeFailFast ends the wait as soon as any job or step has failed,
	// without waiting for jobs GitHub may still be cancelling.
	waitModeFailFast waitMode = iota
	// waitModeAllJobs ends the wait only once every job has reached a terminal
	// state, whatever its conclusion.
	waitModeAllJobs
)

// waitReason identifies which condition ended a wait, so the caller can log the
// same message the condition has always produced.
type waitReason int

const (
	waitReasonNotDone waitReason = iota
	waitReasonJobFailed
	waitReasonRunCompleted
	waitReasonAllJobsDone
)

// waitOutcome is the decision drawn from a single poll. It is computed by
// nextWaitAction, a pure function, so the polling loop itself stays trivial.
type waitOutcome struct {
	done       bool
	conclusion string
	reason     waitReason
}

// WaitForWorkflowRun polls a workflow run until its status is "completed".
//
// pollInterval and maxWait are in seconds; either being zero or negative selects
// the default (5s and 600s). On timeout it returns a result with TimedOut set
// plus a non-nil error. On cancellation it returns the partial result and
// ctx.Err(). A failed API poll returns a nil result.
func (c *Client) WaitForWorkflowRun(ctx context.Context, runID int64, pollInterval int, maxWait int) (*WaitResult, error) {
	const defaultPollInterval = 5
	const defaultMaxWait = 600 // 10 minutes

	if pollInterval <= 0 {
		pollInterval = defaultPollInterval
	}
	if maxWait <= 0 {
		maxWait = defaultMaxWait
	}

	pollDuration := time.Duration(pollInterval) * time.Second
	maxDuration := time.Duration(maxWait) * time.Second
	startTime := time.Now()
	result := &WaitResult{}

	for {
		if err := ctx.Err(); err != nil {
			return result, err
		}

		if maxDuration > 0 && time.Since(startTime) > maxDuration {
			result.TimedOut = true
			result.Elapsed = time.Since(startTime)
			return result, fmt.Errorf("workflow run %d did not complete within %d seconds", runID, maxWait)
		}

		run, err := c.GetWorkflowRun(ctx, runID)
		if err != nil {
			return nil, fmt.Errorf("failed to get workflow run %d: %w", runID, err)
		}
		result.Run = run
		result.PollCount++

		if run.Status == "completed" {
			return result, nil
		}

		log.Debugf("Workflow run %d status: %s (polling in %v)", runID, run.Status, pollDuration)

		if err := sleepContext(ctx, pollDuration); err != nil {
			return result, err
		}
	}
}

// WaitForRun waits for a workflow run to finish, returning as soon as any job or
// step has failed rather than waiting for the remaining jobs. Polling is silent.
//
// timeoutMinutes of zero or less selects 30 minutes. See waitForRun for the
// result and error contract.
func (c *Client) WaitForRun(ctx context.Context, runID int64, timeoutMinutes int) (*WaitRunResult, error) {
	return c.waitForRun(ctx, runID, timeoutMinutes, waitModeFailFast)
}

// WaitForAll waits for every job in a workflow run to reach a terminal state.
// Unlike WaitForRun, a failed or cancelled job does not end the wait early.
//
// timeoutMinutes of zero or less selects 30 minutes. See waitForRun for the
// result and error contract.
func (c *Client) WaitForAll(ctx context.Context, runID int64, timeoutMinutes int) (*WaitRunResult, error) {
	return c.waitForRun(ctx, runID, timeoutMinutes, waitModeAllJobs)
}

// waitForRun polls run runID every 15 seconds until mode says it is finished.
//
// Contract:
//   - finished        → Status "completed", nil error
//   - deadline passed → Status "timed_out"; the error is nil if the final run
//     state could still be read, non-nil otherwise
//   - ctx cancelled   → Status "cancelled" plus ctx.Err()
//   - API failure     → nil result plus the wrapped error
func (c *Client) waitForRun(ctx context.Context, runID int64, timeoutMinutes int, mode waitMode) (*WaitRunResult, error) { //nolint:nestif // Poll decisions remain together to preserve timing semantics.
	const defaultTimeoutMinutes = 30
	const pollIntervalSeconds = 15

	if timeoutMinutes <= 0 {
		timeoutMinutes = defaultTimeoutMinutes
	}

	pollDuration := time.Duration(pollIntervalSeconds) * time.Second
	maxDuration := time.Duration(timeoutMinutes) * time.Minute
	startTime := time.Now()

	log.Infof("Starting to wait for workflow run %d (timeout: %dm)", runID, timeoutMinutes)

	for {
		if err := ctx.Err(); err != nil {
			return cancelledRunResult(startTime), err
		}

		if elapsed := time.Since(startTime); elapsed > maxDuration {
			return c.timedOutRunResult(ctx, runID, timeoutMinutes, elapsed)
		}

		run, err := c.GetWorkflowRun(ctx, runID)
		if err != nil {
			return nil, fmt.Errorf("failed to get workflow run %d: %w", runID, err)
		}

		jobs, err := c.jobsForWaitDecision(ctx, runID, mode, run)
		if err != nil {
			return nil, err
		}

		if outcome := nextWaitAction(mode, run, jobs); outcome.done {
			elapsed := time.Since(startTime)
			logWaitOutcome(runID, outcome, elapsed)
			return completedRunResult(run, outcome.conclusion, elapsed), nil
		}

		// Silent between polls: callers stream this over MCP.
		if err := sleepContext(ctx, pollDuration); err != nil {
			return cancelledRunResult(startTime), err
		}
	}
}

// jobsForWaitDecision fetches the jobs nextWaitAction needs, and only those:
// fail-fast mode skips the call once the run itself is completed, because the run
// conclusion already settles the question.
func (c *Client) jobsForWaitDecision(ctx context.Context, runID int64, mode waitMode, run *WorkflowRun) ([]*Job, error) {
	if mode == waitModeFailFast && run.Status == "completed" {
		return nil, nil
	}
	jobs, err := c.GetWorkflowJobs(ctx, runID, "", 0)
	if err != nil {
		return nil, fmt.Errorf("failed to get jobs for run %d: %w", runID, err)
	}
	return jobs, nil
}

// nextWaitAction decides whether one poll's observations end the wait. It is
// pure: no I/O, no clock, no logging — which is what makes the decision table
// testable without a server.
//
// jobs may be nil when jobsForWaitDecision skipped the call.
func nextWaitAction(mode waitMode, run *WorkflowRun, jobs []*Job) waitOutcome {
	if mode == waitModeAllJobs { //nolint:nestif // This is the complete decision table for all-jobs mode.
		if len(jobs) > 0 {
			if allJobsCompleted(jobs) {
				return waitOutcome{done: true, conclusion: run.Conclusion, reason: waitReasonAllJobsDone}
			}
			return waitOutcome{}
		}
		// No jobs reported yet: the run status is the only signal available.
		if run.Status == "completed" {
			return waitOutcome{done: true, conclusion: run.Conclusion, reason: waitReasonAllJobsDone}
		}
		return waitOutcome{}
	}

	if run.Status == "completed" {
		return waitOutcome{done: true, conclusion: run.Conclusion, reason: waitReasonRunCompleted}
	}
	if conclusion, failed := firstFailedJobConclusion(jobs); failed {
		return waitOutcome{done: true, conclusion: conclusion, reason: waitReasonJobFailed}
	}
	return waitOutcome{}
}

// allJobsCompleted reports whether every job has status "completed", regardless
// of conclusion.
func allJobsCompleted(jobs []*Job) bool {
	for _, job := range jobs {
		if job.Status != "completed" {
			return false
		}
	}
	return true
}

// firstFailedJobConclusion returns the conclusion of the first job that has
// failed or timed out, falling back to the conclusion of its first failing step
// when the job itself has no conclusion yet.
func firstFailedJobConclusion(jobs []*Job) (string, bool) {
	for _, job := range jobs {
		jobFailed := job.Conclusion == "failure" || job.Conclusion == "timed_out"
		stepConclusion := firstFailedStepConclusion(job.Steps)
		if !jobFailed && stepConclusion == "" {
			continue
		}
		if job.Conclusion != "" {
			return job.Conclusion, true
		}
		return stepConclusion, true
	}
	return "", false
}

// firstFailedStepConclusion returns the conclusion of the first failed or
// timed-out step, or "" when no step has failed.
func firstFailedStepConclusion(steps []*Step) string {
	for _, step := range steps {
		if step.Conclusion == "failure" || step.Conclusion == "timed_out" {
			return step.Conclusion
		}
	}
	return ""
}

// logWaitOutcome emits the message that has always accompanied each terminating
// condition. waitModeAllJobs completions are deliberately silent.
func logWaitOutcome(runID int64, outcome waitOutcome, elapsed time.Duration) {
	switch outcome.reason {
	case waitReasonJobFailed:
		log.Infof("Workflow run %d failed: %s (duration: %.1fs)", runID, outcome.conclusion, elapsed.Seconds())
	case waitReasonRunCompleted:
		log.Infof("Workflow run %d completed: %s (duration: %.1fs)", runID, outcome.conclusion, elapsed.Seconds())
	}
}

// completedRunResult builds the successful result for a finished wait.
func completedRunResult(run *WorkflowRun, conclusion string, elapsed time.Duration) *WaitRunResult {
	return &WaitRunResult{
		Status:          "completed",
		Conclusion:      conclusion,
		DurationSeconds: elapsed.Seconds(),
		RunURL:          run.URL,
		StartedAt:       run.CreatedAt,
		CompletedAt:     run.UpdatedAt,
		TimeoutReached:  false,
	}
}

// cancelledRunResult builds the result returned alongside ctx.Err().
func cancelledRunResult(startTime time.Time) *WaitRunResult {
	return &WaitRunResult{
		Status:          "cancelled",
		DurationSeconds: time.Since(startTime).Seconds(),
		TimeoutReached:  false,
	}
}

// timedOutRunResult builds the result for an expired deadline. One last run
// lookup enriches the result; if even that fails, the error is returned so the
// caller can tell "timed out, here is the state" from "timed out, blind".
func (c *Client) timedOutRunResult(ctx context.Context, runID int64, timeoutMinutes int, elapsed time.Duration) (*WaitRunResult, error) {
	run, err := c.GetWorkflowRun(ctx, runID)
	if err != nil {
		return &WaitRunResult{
			Status:          "timed_out",
			DurationSeconds: elapsed.Seconds(),
			TimeoutReached:  true,
		}, fmt.Errorf("workflow run %d did not complete within %d minutes", runID, timeoutMinutes)
	}
	return &WaitRunResult{
		Status:          "timed_out",
		Conclusion:      run.Conclusion,
		DurationSeconds: elapsed.Seconds(),
		RunURL:          run.URL,
		StartedAt:       run.CreatedAt,
		TimeoutReached:  true,
	}, nil
}

// WaitForCommitChecks polls the checks for ref every 15 seconds until every check
// run has completed. An empty ref resolves to the local HEAD commit.
//
// timeoutMinutes of zero or less selects 30 minutes. Contract mirrors waitForRun:
// completed → the aggregate state with a nil error; deadline passed →
// OverallConclusion "timed_out"; ctx cancelled → "cancelled" plus ctx.Err(); API
// failure → nil result plus the wrapped error.
func (c *Client) WaitForCommitChecks(ctx context.Context, ref string, timeoutMinutes int) (*WaitCommitChecksResult, error) { //nolint:gocognit // Polling and terminal-state projection are one loop.
	const defaultTimeoutMinutes = 30
	const pollIntervalSeconds = 15

	if timeoutMinutes <= 0 {
		timeoutMinutes = defaultTimeoutMinutes
	}
	if ref == "" {
		commit, err := GetLastCommit()
		if err != nil {
			return nil, fmt.Errorf("failed to get current commit: %w", err)
		}
		ref = commit.SHA
	}

	pollDuration := time.Duration(pollIntervalSeconds) * time.Second
	maxDuration := time.Duration(timeoutMinutes) * time.Minute
	startTime := time.Now()

	log.Infof("Starting to wait for checks on ref %s (timeout: %dm)", ref, timeoutMinutes)

	for {
		if err := ctx.Err(); err != nil {
			return cancelledChecksResult(startTime), err
		}

		if elapsed := time.Since(startTime); elapsed > maxDuration {
			return c.timedOutChecksResult(ctx, ref, timeoutMinutes, elapsed)
		}

		status, err := c.GetCheckRunsForRef(ctx, ref, &GetCheckRunsOptions{Filter: "all"})
		if err != nil {
			return nil, fmt.Errorf("failed to get check runs: %w", err)
		}

		// No checks registered yet is not "all done": keep polling.
		if len(status.CheckRuns) > 0 && allChecksCompleted(status.CheckRuns) {
			elapsed := time.Since(startTime)
			log.Infof("All checks completed for ref %s: %s (duration: %.1fs)", ref, status.State, elapsed.Seconds())
			return &WaitCommitChecksResult{
				OverallConclusion:  status.State,
				ChecksTotal:        status.TotalCount,
				ChecksByConclusion: copyConclusionCounts(status.ByConclusion),
				DurationSeconds:    elapsed.Seconds(),
				TimeoutReached:     false,
			}, nil
		}

		if err := sleepContext(ctx, pollDuration); err != nil {
			return cancelledChecksResult(startTime), err
		}
	}
}

// allChecksCompleted reports whether every check run has status "completed".
func allChecksCompleted(checkRuns []*CheckRun) bool {
	for _, cr := range checkRuns {
		if cr.Status != "completed" {
			return false
		}
	}
	return true
}

// copyConclusionCounts snapshots the counts so the caller's result does not alias
// the CombinedCheckStatus it came from.
func copyConclusionCounts(byConclusion map[string]int) map[string]int {
	result := make(map[string]int, len(byConclusion))
	for k, v := range byConclusion {
		result[k] = v
	}
	return result
}

// cancelledChecksResult builds the result returned alongside ctx.Err().
func cancelledChecksResult(startTime time.Time) *WaitCommitChecksResult {
	return &WaitCommitChecksResult{
		OverallConclusion: "cancelled",
		DurationSeconds:   time.Since(startTime).Seconds(),
		TimeoutReached:    false,
	}
}

// timedOutChecksResult builds the result for an expired deadline, enriched with
// one last check lookup when that still succeeds.
func (c *Client) timedOutChecksResult(ctx context.Context, ref string, timeoutMinutes int, elapsed time.Duration) (*WaitCommitChecksResult, error) {
	status, err := c.GetCheckRunsForRef(ctx, ref, &GetCheckRunsOptions{Filter: "all"})
	if err != nil {
		return &WaitCommitChecksResult{
			OverallConclusion: "timed_out",
			DurationSeconds:   elapsed.Seconds(),
			TimeoutReached:    true,
		}, fmt.Errorf("checks did not complete within %d minutes", timeoutMinutes)
	}
	return &WaitCommitChecksResult{
		OverallConclusion:  "timed_out",
		ChecksTotal:        status.TotalCount,
		ChecksByConclusion: copyConclusionCounts(status.ByConclusion),
		DurationSeconds:    elapsed.Seconds(),
		TimeoutReached:     true,
	}, nil
}

// ManageRun cancels, reruns, or reruns the failed jobs of a workflow run.
//
// It returns an error only for an unknown action; an API failure is reported as
// a result with Status "failed" and the API error in Message. Note that GitHub
// answers a successful cancellation with 202 Accepted, which the underlying
// client surfaces as an error — see TestCancelWorkflowRun_202IsReportedAsAnError.
func (c *Client) ManageRun(ctx context.Context, runID int64, action ManageRunAction) (*ManageRunResult, error) {
	var err error
	var message string

	switch action {
	case ManageRunActionCancel:
		_, err = c.gh.Actions.CancelWorkflowRunByID(ctx, c.owner, c.repo, runID)
		if err == nil {
			message = fmt.Sprintf("Successfully cancelled workflow run %d", runID)
		}
	case ManageRunActionRerun:
		_, err = c.gh.Actions.RerunWorkflowByID(ctx, c.owner, c.repo, runID)
		if err == nil {
			message = fmt.Sprintf("Successfully triggered rerun for workflow run %d", runID)
		}
	case ManageRunActionRerunFailed:
		_, err = c.gh.Actions.RerunFailedJobsByID(ctx, c.owner, c.repo, runID)
		if err == nil {
			message = fmt.Sprintf("Successfully triggered rerun of failed jobs for workflow run %d", runID)
		}
	default:
		return nil, fmt.Errorf("unknown action: %s", action)
	}

	if err != nil {
		//nolint:nilerr // API failures are part of the tool result contract, not transport errors.
		return &ManageRunResult{
			RunID:   runID,
			Action:  action,
			Status:  "failed",
			Message: err.Error(),
		}, nil
	}

	return &ManageRunResult{
		RunID:   runID,
		Action:  action,
		Status:  "success",
		Message: message,
	}, nil
}
