package github

import (
	"context"
	"fmt"
	"time"
)

// WaitRunResult is the result of waiting for a workflow run
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

// WaitCommitChecksResult is the result of waiting for commit checks
type WaitCommitChecksResult struct {
	OverallConclusion  string         `json:"overall_conclusion"` // "success", "failure", "pending", "neutral"
	ChecksTotal        int            `json:"checks_total"`
	ChecksByConclusion map[string]int `json:"checks_by_conclusion"`
	DurationSeconds    float64        `json:"duration_seconds"`
	TimeoutReached     bool           `json:"timeout_reached"`
}

// ManageRunAction represents an action to take on a workflow run
type ManageRunAction string

const (
	ManageRunActionCancel      ManageRunAction = "cancel"
	ManageRunActionRerun       ManageRunAction = "rerun"
	ManageRunActionRerunFailed ManageRunAction = "rerun_failed"
)

// ManageRunResult is the result of managing a workflow run
type ManageRunResult struct {
	RunID   int64           `json:"run_id"`
	Action  ManageRunAction `json:"action"`
	Status  string          `json:"status"` // "success", "failed"
	Message string          `json:"message,omitempty"`
}

type WaitResult struct {
	Run       *WorkflowRun
	TimedOut  bool
	Elapsed   time.Duration
	PollCount int
}

// WaitForWorkflowRun polls a workflow run until it completes (success, failure, cancelled, etc.)
// pollInterval is the time between polls in seconds
// maxWait is the maximum time to wait in seconds (0 for no limit)
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
		// Check context cancellation
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		default:
		}

		// Check timeout
		if maxDuration > 0 && time.Since(startTime) > maxDuration {
			result.TimedOut = true
			result.Elapsed = time.Since(startTime)
			return result, fmt.Errorf("workflow run %d did not complete within %d seconds", runID, maxWait)
		}

		// Get current status
		run, err := c.GetWorkflowRun(ctx, runID)
		if err != nil {
			return nil, fmt.Errorf("failed to get workflow run %d: %w", runID, err)
		}
		result.Run = run
		result.PollCount++

		// Check if completed
		if run.Status == "completed" {
			return result, nil
		}

		log.Debugf("Workflow run %d status: %s (polling in %v)", runID, run.Status, pollDuration)

		// Wait before next poll
		timer := time.NewTimer(pollDuration)
		select {
		case <-ctx.Done():
			timer.Stop()
			return result, ctx.Err()
		case <-timer.C:
		}
	}
}

// WaitForRun waits for a workflow run to complete, returning early when a job
// fails (silent polling).
func (c *Client) WaitForRun(ctx context.Context, runID int64, timeoutMinutes int) (*WaitRunResult, error) {
	return c.waitForRun(ctx, runID, timeoutMinutes, false)
}

// WaitForAll waits for every job in a workflow run to reach a terminal state.
// Unlike WaitForRun, a failed or cancelled job does not end the wait early.
func (c *Client) WaitForAll(ctx context.Context, runID int64, timeoutMinutes int) (*WaitRunResult, error) {
	return c.waitForRun(ctx, runID, timeoutMinutes, true)
}

func (c *Client) waitForRun(ctx context.Context, runID int64, timeoutMinutes int, waitAll bool) (*WaitRunResult, error) {
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
		// Check context cancellation
		select {
		case <-ctx.Done():
			return &WaitRunResult{
				Status:          "cancelled",
				DurationSeconds: time.Since(startTime).Seconds(),
				TimeoutReached:  false,
			}, ctx.Err()
		default:
		}

		// Check timeout
		elapsed := time.Since(startTime)
		if elapsed > maxDuration {
			// Get final run state for the result
			run, err := c.GetWorkflowRun(ctx, runID)
			if err == nil {
				return &WaitRunResult{
					Status:          "timed_out",
					Conclusion:      run.Conclusion,
					DurationSeconds: elapsed.Seconds(),
					RunURL:          run.URL,
					StartedAt:       run.CreatedAt,
					TimeoutReached:  true,
				}, nil
			}
			return &WaitRunResult{
				Status:          "timed_out",
				DurationSeconds: elapsed.Seconds(),
				TimeoutReached:  true,
			}, fmt.Errorf("workflow run %d did not complete within %d minutes", runID, timeoutMinutes)
		}

		// Get current status
		run, err := c.GetWorkflowRun(ctx, runID)
		if err != nil {
			return nil, fmt.Errorf("failed to get workflow run %d: %w", runID, err)
		}

		// A normal wait is fail-fast: once a job has failed, there is no reason
		// to wait for unrelated jobs that GitHub may still be cancelling.
		if !waitAll && run.Status != "completed" {
			jobs, err := c.GetWorkflowJobs(ctx, runID, "", 0)
			if err != nil {
				return nil, fmt.Errorf("failed to get jobs for run %d: %w", runID, err)
			}
			for _, job := range jobs {
				failedConclusion := job.Conclusion == "failure" || job.Conclusion == "timed_out"
				failedStep := ""
				for _, step := range job.Steps {
					if step.Conclusion == "failure" || step.Conclusion == "timed_out" {
						failedStep = step.Conclusion
						break
					}
				}
				if failedConclusion || failedStep != "" {
					elapsed := time.Since(startTime)
					conclusion := job.Conclusion
					if conclusion == "" {
						conclusion = failedStep
					}
					log.Infof("Workflow run %d failed: %s (duration: %.1fs)", runID, conclusion, elapsed.Seconds())
					return &WaitRunResult{
						Status:          "completed",
						Conclusion:      conclusion,
						DurationSeconds: elapsed.Seconds(),
						RunURL:          run.URL,
						StartedAt:       run.CreatedAt,
						CompletedAt:     run.UpdatedAt,
						TimeoutReached:  false,
					}, nil
				}
			}
		}

		// Check if the run (or, for wait_all, every job) is completed.
		if run.Status == "completed" && !waitAll {
			elapsed := time.Since(startTime)
			log.Infof("Workflow run %d completed: %s (duration: %.1fs)", runID, run.Conclusion, elapsed.Seconds())
			return &WaitRunResult{
				Status:          "completed",
				Conclusion:      run.Conclusion,
				DurationSeconds: elapsed.Seconds(),
				RunURL:          run.URL,
				StartedAt:       run.CreatedAt,
				CompletedAt:     run.UpdatedAt,
				TimeoutReached:  false,
			}, nil
		}
		if waitAll {
			jobs, err := c.GetWorkflowJobs(ctx, runID, "", 0)
			if err != nil {
				return nil, fmt.Errorf("failed to get jobs for run %d: %w", runID, err)
			}
			if len(jobs) > 0 {
				allCompleted := true
				for _, job := range jobs {
					if job.Status != "completed" {
						allCompleted = false
						break
					}
				}
				if allCompleted {
					elapsed := time.Since(startTime)
					return &WaitRunResult{
						Status:          "completed",
						Conclusion:      run.Conclusion,
						DurationSeconds: elapsed.Seconds(),
						RunURL:          run.URL,
						StartedAt:       run.CreatedAt,
						CompletedAt:     run.UpdatedAt,
						TimeoutReached:  false,
					}, nil
				}
			} else if run.Status == "completed" {
				return &WaitRunResult{
					Status:          "completed",
					Conclusion:      run.Conclusion,
					DurationSeconds: time.Since(startTime).Seconds(),
					RunURL:          run.URL,
					StartedAt:       run.CreatedAt,
					CompletedAt:     run.UpdatedAt,
					TimeoutReached:  false,
				}, nil
			}
		}

		// Wait before next poll (silent - no log during polling)
		timer := time.NewTimer(pollDuration)
		select {
		case <-ctx.Done():
			timer.Stop()
			return &WaitRunResult{
				Status:          "cancelled",
				DurationSeconds: time.Since(startTime).Seconds(),
				TimeoutReached:  false,
			}, ctx.Err()
		case <-timer.C:
		}
	}
}

// WaitForCommitChecks waits for all check runs for a commit to complete
func (c *Client) WaitForCommitChecks(ctx context.Context, ref string, timeoutMinutes int) (*WaitCommitChecksResult, error) {
	const defaultTimeoutMinutes = 30
	const pollIntervalSeconds = 15

	if timeoutMinutes <= 0 {
		timeoutMinutes = defaultTimeoutMinutes
	}

	if ref == "" {
		// Try to get HEAD SHA
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
		select {
		case <-ctx.Done():
			return &WaitCommitChecksResult{
				OverallConclusion: "cancelled",
				DurationSeconds:   time.Since(startTime).Seconds(),
				TimeoutReached:    false,
			}, ctx.Err()
		default:
		}

		elapsed := time.Since(startTime)
		if elapsed > maxDuration {
			status, err := c.GetCheckRunsForRef(ctx, ref, &GetCheckRunsOptions{Filter: "all"})
			if err == nil {
				byConclusion := make(map[string]int)
				for k, v := range status.ByConclusion {
					byConclusion[k] = v
				}
				return &WaitCommitChecksResult{
					OverallConclusion:  "timed_out",
					ChecksTotal:        status.TotalCount,
					ChecksByConclusion: byConclusion,
					DurationSeconds:    elapsed.Seconds(),
					TimeoutReached:     true,
				}, nil
			}
			return &WaitCommitChecksResult{
				OverallConclusion: "timed_out",
				DurationSeconds:   elapsed.Seconds(),
				TimeoutReached:    true,
			}, fmt.Errorf("checks did not complete within %d minutes", timeoutMinutes)
		}

		status, err := c.GetCheckRunsForRef(ctx, ref, &GetCheckRunsOptions{Filter: "all"})
		if err != nil {
			return nil, fmt.Errorf("failed to get check runs: %w", err)
		}

		// Check if all checks are complete (skip if no checks registered yet)
		if len(status.CheckRuns) > 0 {
			allComplete := true
			for _, cr := range status.CheckRuns {
				if cr.Status != "completed" {
					allComplete = false
					break
				}
			}

			if allComplete {
				elapsed := time.Since(startTime)
				byConclusion := make(map[string]int)
				for k, v := range status.ByConclusion {
					byConclusion[k] = v
				}
				log.Infof("All checks completed for ref %s: %s (duration: %.1fs)", ref, status.State, elapsed.Seconds())
				return &WaitCommitChecksResult{
					OverallConclusion:  status.State,
					ChecksTotal:        status.TotalCount,
					ChecksByConclusion: byConclusion,
					DurationSeconds:    elapsed.Seconds(),
					TimeoutReached:     false,
				}, nil
			}
		}

		timer := time.NewTimer(pollDuration)
		select {
		case <-ctx.Done():
			timer.Stop()
			return &WaitCommitChecksResult{
				OverallConclusion: "cancelled",
				DurationSeconds:   time.Since(startTime).Seconds(),
				TimeoutReached:    false,
			}, ctx.Err()
		case <-timer.C:
		}
	}
}

// ManageRun performs an action on a workflow run (cancel, rerun, or rerun_failed)
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
