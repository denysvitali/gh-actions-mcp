package github

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/go-github/v69/github"
	"github.com/sirupsen/logrus"
)

var log *logrus.Logger

func SetLogger(l *logrus.Logger) {
	log = l
}

type Client struct {
	owner string
	repo  string
	gh    *github.Client
}

func NewClient(token, owner, repo string) *Client {
	hc := &http.Client{}
	gh := github.NewClient(hc)
	gh = gh.WithAuthToken(token)
	return &Client{
		owner: owner,
		repo:  repo,
		gh:    gh,
	}
}

type WorkflowRun struct {
	ID         int64    `json:"id"`
	Name       string   `json:"name"`
	Status     string   `json:"status"`
	Conclusion string   `json:"conclusion"`
	Branch     string   `json:"branch"`
	Event      string   `json:"event"`
	Actor      string   `json:"actor"`
	CreatedAt  string   `json:"created_at"`
	UpdatedAt  string   `json:"updated_at"`
	URL        string   `json:"url"`
	RunNumber  int      `json:"run_number"`
	WorkflowID int64    `json:"workflow_id"`
}

type Workflow struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Path  string `json:"path"`
	State string `json:"state"`
}

type ActionsStatus struct {
	TotalWorkflows   int            `json:"total_workflows"`
	TotalRuns        int            `json:"total_runs"`
	RecentRuns       []*WorkflowRun `json:"recent_runs"`
	SuccessfulRuns   int            `json:"successful_runs"`
	FailedRuns       int            `json:"failed_runs"`
	InProgressRuns   int            `json:"in_progress_runs"`
	QueuedRuns       int            `json:"queued_runs"`
	PendingRuns      int            `json:"pending_runs"`
}

func (c *Client) GetActionsStatus(ctx context.Context, limit int) (*ActionsStatus, error) {
	status := &ActionsStatus{}

	// Get workflows
	workflows, _, err := c.gh.Actions.ListWorkflows(ctx, c.owner, c.repo, &github.ListOptions{PerPage: 100})
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
		wr := &WorkflowRun{
			ID:         run.GetID(),
			Name:       run.GetName(),
			Status:     run.GetStatus(),
			Conclusion: run.GetConclusion(),
			Branch:     run.GetHeadBranch(),
			Event:      run.GetEvent(),
			Actor:      run.GetActor().GetLogin(),
			CreatedAt:  run.GetCreatedAt().String(),
			UpdatedAt:  run.GetUpdatedAt().String(),
			URL:        run.GetHTMLURL(),
			RunNumber:  run.GetRunNumber(),
			WorkflowID: run.GetWorkflowID(),
		}
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

func (c *Client) GetWorkflowRuns(ctx context.Context, workflowID int64) ([]*WorkflowRun, error) {
	runs, _, err := c.gh.Actions.ListWorkflowRunsByID(ctx, c.owner, c.repo, workflowID, &github.ListWorkflowRunsOptions{
		ListOptions: github.ListOptions{PerPage: 50},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list workflow runs for workflow %d: %w", workflowID, err)
	}

	result := make([]*WorkflowRun, 0, len(runs.WorkflowRuns))
	for _, run := range runs.WorkflowRuns {
		wr := &WorkflowRun{
			ID:         run.GetID(),
			Name:       run.GetName(),
			Status:     run.GetStatus(),
			Conclusion: run.GetConclusion(),
			Branch:     run.GetHeadBranch(),
			Event:      run.GetEvent(),
			Actor:      run.GetActor().GetLogin(),
			CreatedAt:  run.GetCreatedAt().String(),
			UpdatedAt:  run.GetUpdatedAt().String(),
			URL:        run.GetHTMLURL(),
			RunNumber:  run.GetRunNumber(),
			WorkflowID: run.GetWorkflowID(),
		}
		result = append(result, wr)
	}

	return result, nil
}

func (c *Client) GetWorkflows(ctx context.Context) ([]*Workflow, error) {
	workflows, _, err := c.gh.Actions.ListWorkflows(ctx, c.owner, c.repo, &github.ListOptions{PerPage: 100})
	if err != nil {
		return nil, fmt.Errorf("failed to list workflows: %w", err)
	}

	result := make([]*Workflow, len(workflows.Workflows))
	for i, w := range workflows.Workflows {
		result[i] = &Workflow{
			ID:    w.GetID(),
			Name:  w.GetName(),
			Path:  w.GetPath(),
			State: w.GetState(),
		}
	}

	return result, nil
}

func (c *Client) TriggerWorkflow(ctx context.Context, workflowID string, ref string) error {
	// Try to parse as ID first
	if id, err := parseWorkflowID(workflowID); err == nil {
		_, err := c.gh.Actions.CreateWorkflowDispatchEventByID(ctx, c.owner, c.repo, id, github.CreateWorkflowDispatchEventRequest{
			Ref: ref,
		})
		if err != nil {
			return fmt.Errorf("failed to trigger workflow %s: %w", workflowID, err)
		}
		return nil
	}

	// Try by name
	workflows, _, err := c.gh.Actions.ListWorkflows(ctx, c.owner, c.repo, &github.ListOptions{PerPage: 100})
	if err != nil {
		return fmt.Errorf("failed to list workflows: %w", err)
	}

	for _, w := range workflows.Workflows {
		if w.GetName() == workflowID || w.GetPath() == workflowID {
			_, err := c.gh.Actions.CreateWorkflowDispatchEventByID(ctx, c.owner, c.repo, w.GetID(), github.CreateWorkflowDispatchEventRequest{
				Ref: ref,
			})
			if err != nil {
				return fmt.Errorf("failed to trigger workflow %s: %w", workflowID, err)
			}
			return nil
		}
	}

	return fmt.Errorf("workflow %s not found", workflowID)
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

func (c *Client) GetRepoInfo() (string, string) {
	return c.owner, c.repo
}

// InferRepoFromOrigin attempts to extract owner/repo from a git remote URL
func InferRepoFromOrigin(remoteURL string) (owner, repo string, err error) {
	// Handle SSH format: git@github.com:owner/repo.git
	if strings.Contains(remoteURL, "git@") {
		parts := strings.Split(remoteURL, ":")
		if len(parts) > 1 {
			path := strings.TrimSuffix(parts[1], ".git")
			repoParts := strings.Split(path, "/")
			if len(repoParts) == 2 {
				return repoParts[0], repoParts[1], nil
			}
		}
	}

	// Handle HTTPS format: https://github.com/owner/repo.git
	if strings.HasPrefix(remoteURL, "https://") || strings.HasPrefix(remoteURL, "http://") {
		path := strings.TrimPrefix(remoteURL, "https://")
		path = strings.TrimPrefix(path, "http://")
		path = strings.TrimPrefix(path, "github.com/")
		path = strings.TrimSuffix(path, ".git")
		repoParts := strings.Split(path, "/")
		if len(repoParts) == 2 {
			return repoParts[0], repoParts[1], nil
		}
	}

	return "", "", fmt.Errorf("could not parse owner/repo from URL: %s", remoteURL)
}

func parseWorkflowID(id string) (int64, error) {
	return strconv.ParseInt(id, 10, 64)
}
