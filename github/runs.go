package github

import (
	"context"
	"fmt"

	"github.com/google/go-github/v89/github"
)

// ListRunsOptions contains parameters for listing workflow runs
type ListRunsOptions struct {
	WorkflowID   *int64 // Optional: filter by workflow ID
	Branch       string // Optional: filter by branch
	Status       string // Optional: queued, in_progress, completed, etc.
	Conclusion   string // Optional: success, failure, neutral, cancelled, etc.
	Per_page     int    // Optional: number of results per page
	CreatedAfter string // Optional: ISO 8601 date string
	Event        string // Optional: push, pull_request, etc.
	Actor        string // Optional: GitHub username
	Page         int    // Optional: one-based GitHub API page
}

type ActionsStatus struct {
	TotalWorkflows int            `json:"total_workflows"`
	TotalRuns      int            `json:"total_runs"`
	RecentRuns     []*WorkflowRun `json:"recent_runs"`
	SuccessfulRuns int            `json:"successful_runs"`
	FailedRuns     int            `json:"failed_runs"`
	InProgressRuns int            `json:"in_progress_runs"`
	QueuedRuns     int            `json:"queued_runs"`
	PendingRuns    int            `json:"pending_runs"`
}

func (c *Client) GetActionsStatus(ctx context.Context, limit int) (*ActionsStatus, error) {
	status := &ActionsStatus{}

	// Get workflows
	workflows, _, err := c.gh.Actions.ListWorkflows(ctx, c.owner, c.repo, &github.ListOptions{PerPage: c.perPageLimit})
	if err != nil {
		return nil, fmt.Errorf("failed to list workflows: %w", err)
	}
	status.TotalWorkflows = len(workflows.Workflows)

	// Get recent workflow runs
	runs, _, err := c.gh.Actions.ListRepositoryWorkflowRuns(ctx, c.owner, c.repo, &github.ListWorkflowRunsOptions{
		ListOptions: github.ListOptions{PerPage: limit},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list workflow runs: %w", err)
	}
	status.TotalRuns = runs.GetTotalCount()

	for _, run := range runs.WorkflowRuns {
		wr := workflowRunFromGitHub(run)
		status.RecentRuns = append(status.RecentRuns, wr)

		switch wr.Conclusion {
		case "success":
			status.SuccessfulRuns++
		case "failure", "cancelled", "timed_out", "action_required":
			status.FailedRuns++
		}

		switch wr.Status {
		case "in_progress":
			status.InProgressRuns++
		case "queued":
			status.QueuedRuns++
		case "pending":
			status.PendingRuns++
		}
	}

	log.Debugf("Retrieved status for %s/%s: %d workflows, %d runs",
		c.owner, c.repo, status.TotalWorkflows, status.TotalRuns)

	return status, nil
}

func (c *Client) GetWorkflowRun(ctx context.Context, runID int64) (*WorkflowRun, error) {
	run, _, err := c.gh.Actions.GetWorkflowRunByID(ctx, c.owner, c.repo, runID)
	if err != nil {
		return nil, fmt.Errorf("failed to get workflow run %d: %w", runID, err)
	}

	return workflowRunFromGitHub(run), nil
}

func (c *Client) GetWorkflowRuns(ctx context.Context, workflowID int64, branch string) ([]*WorkflowRun, error) {
	opts := &github.ListWorkflowRunsOptions{
		ListOptions: github.ListOptions{PerPage: c.perPageLimit},
	}

	if branch != "" {
		opts.Branch = branch
	}

	runs, _, err := c.gh.Actions.ListWorkflowRunsByID(ctx, c.owner, c.repo, workflowID, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to list workflow runs for workflow %d: %w", workflowID, err)
	}

	result := make([]*WorkflowRun, 0, len(runs.WorkflowRuns))
	for _, run := range runs.WorkflowRuns {
		result = append(result, workflowRunFromGitHub(run))
	}

	return result, nil
}

func (c *Client) CancelWorkflowRun(ctx context.Context, runID int64) error {
	_, err := c.gh.Actions.CancelWorkflowRunByID(ctx, c.owner, c.repo, runID)
	if err != nil {
		return fmt.Errorf("failed to cancel workflow run %d: %w", runID, err)
	}
	return nil
}

func (c *Client) RerunWorkflowRun(ctx context.Context, runID int64) error {
	_, err := c.gh.Actions.RerunWorkflowByID(ctx, c.owner, c.repo, runID)
	if err != nil {
		return fmt.Errorf("failed to rerun workflow run %d: %w", runID, err)
	}
	return nil
}

// ListRepositoryWorkflowRunsWithOptions lists workflow runs with comprehensive filtering options
func (c *Client) ListRepositoryWorkflowRunsWithOptions(ctx context.Context, opts *ListRunsOptions) ([]*WorkflowRun, error) {
	if opts == nil {
		opts = &ListRunsOptions{}
	}
	limit := opts.Per_page
	if limit <= 0 {
		limit = c.perPageLimit
	}
	page := opts.Page
	if page <= 0 {
		page = 1
	}
	result := make([]*WorkflowRun, 0, limit)
	options := *opts
	for page > 0 && len(result) < limit {
		options.Page = page
		runs, nextPage, err := c.ListRepositoryWorkflowRunsPage(ctx, &options)
		if err != nil {
			return nil, err
		}
		result = append(result, runs...)
		page = nextPage
	}
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

// ListRepositoryWorkflowRunsPage returns one page of workflow runs and the
// next GitHub page, or zero when there are no more pages.
func (c *Client) ListRepositoryWorkflowRunsPage(ctx context.Context, opts *ListRunsOptions) ([]*WorkflowRun, int, error) {
	if opts == nil {
		opts = &ListRunsOptions{}
	}
	githubOpts := &github.ListWorkflowRunsOptions{
		ListOptions: github.ListOptions{},
	}

	// Apply optional filters
	if opts.Branch != "" {
		githubOpts.Branch = opts.Branch
	}
	if opts.Status != "" {
		githubOpts.Status = opts.Status
	}
	// Note: Conclusion filtering needs to be done client-side
	// if opts.Conclusion != "" {
	// 	githubOpts.Conclusion = opts.Conclusion
	// }
	if opts.CreatedAfter != "" {
		githubOpts.Created = opts.CreatedAfter
	}
	if opts.Event != "" {
		githubOpts.Event = opts.Event
	}
	if opts.Actor != "" {
		githubOpts.Actor = opts.Actor
	}

	per_page := c.perPageLimit
	if opts.Per_page > 0 {
		per_page = opts.Per_page
	}
	githubOpts.ListOptions.PerPage = per_page
	if opts.Page > 0 {
		githubOpts.ListOptions.Page = opts.Page
	}

	var runs *github.WorkflowRuns
	var response *github.Response
	var err error

	if opts.WorkflowID != nil {
		// List runs for a specific workflow
		runs, response, err = c.gh.Actions.ListWorkflowRunsByID(ctx, c.owner, c.repo, *opts.WorkflowID, githubOpts)
	} else {
		// List all repository workflow runs
		runs, response, err = c.gh.Actions.ListRepositoryWorkflowRuns(ctx, c.owner, c.repo, githubOpts)
	}

	if err != nil {
		return nil, 0, fmt.Errorf("failed to list workflow runs: %w", err)
	}

	result := make([]*WorkflowRun, 0, len(runs.WorkflowRuns))
	for _, run := range runs.WorkflowRuns {
		// Apply conclusion filter client-side if needed
		if opts.Conclusion != "" && run.GetConclusion() != opts.Conclusion {
			continue
		}
		result = append(result, workflowRunFromGitHub(run))
	}

	nextPage := 0
	if response != nil {
		nextPage = response.NextPage
	}
	return result, nextPage, nil
}

// GetWorkflowJobs retrieves jobs for a workflow run
func (c *Client) GetWorkflowJobs(ctx context.Context, runID int64, filter string, attemptNumber int) ([]*Job, error) {
	opts := &github.ListWorkflowJobsOptions{
		ListOptions: github.ListOptions{PerPage: c.perPageLimit},
	}

	if filter != "" {
		opts.Filter = filter
	}

	jobs, _, err := c.gh.Actions.ListWorkflowJobs(ctx, c.owner, c.repo, runID, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to list jobs for run %d: %w", runID, err)
	}

	result := make([]*Job, 0, len(jobs.Jobs))
	for _, job := range jobs.Jobs {
		// Filter by attempt number if specified
		if attemptNumber > 0 && job.GetRunAttempt() != int64(attemptNumber) {
			continue
		}

		var labels []string
		if job.Labels != nil {
			labels = job.Labels
		}

		steps := make([]*Step, 0, len(job.Steps))
		for _, s := range job.Steps {
			steps = append(steps, &Step{
				Name:            s.GetName(),
				Number:          s.GetNumber(),
				Status:          s.GetStatus(),
				Conclusion:      s.GetConclusion(),
				StartedAt:       formatTime(s.StartedAt),
				CompletedAt:     formatTime(s.CompletedAt),
				DurationSeconds: durationSeconds(s.StartedAt, s.CompletedAt),
			})
		}

		result = append(result, &Job{
			ID:              job.GetID(),
			Name:            job.GetName(),
			Status:          job.GetStatus(),
			Conclusion:      job.GetConclusion(),
			StartedAt:       formatTime(job.StartedAt),
			CompletedAt:     formatTime(job.CompletedAt),
			DurationSeconds: durationSeconds(job.StartedAt, job.CompletedAt),
			RunnerName:      job.GetRunnerName(),
			RunnerGroup:     job.GetRunnerGroupName(),
			Labels:          labels,
			WorkflowRunID:   job.GetRunID(),
			Steps:           steps,
		})
	}

	return result, nil
}
