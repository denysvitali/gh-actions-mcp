package github

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

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
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	Branch     string `json:"branch"`
	Event      string `json:"event"`
	Actor      string `json:"actor"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
	URL        string `json:"url"`
	RunNumber  int    `json:"run_number"`
	WorkflowID int64  `json:"workflow_id"`
}

type Workflow struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Path  string `json:"path"`
	State string `json:"state"`
}

// workflowRunFromGitHub converts a github.WorkflowRun to our WorkflowRun type
func workflowRunFromGitHub(run *github.WorkflowRun) *WorkflowRun {
	return &WorkflowRun{
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

func (c *Client) GetWorkflowRuns(ctx context.Context, workflowID int64) ([]*WorkflowRun, error) {
	runs, _, err := c.gh.Actions.ListWorkflowRunsByID(ctx, c.owner, c.repo, workflowID, &github.ListWorkflowRunsOptions{
		ListOptions: github.ListOptions{PerPage: 50},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list workflow runs for workflow %d: %w", workflowID, err)
	}

	result := make([]*WorkflowRun, 0, len(runs.WorkflowRuns))
	for _, run := range runs.WorkflowRuns {
		result = append(result, workflowRunFromGitHub(run))
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

// GetWorkflowLogs retrieves the logs for a workflow run and returns them as a string.
// The logs can be limited by line count using head or tail parameters.
// If both are specified, tail takes precedence.
func (c *Client) GetWorkflowLogs(ctx context.Context, runID int64, head, tail int) (string, error) {
	// Get the log archive (GitHub returns a redirect to a ZIP file)
	url, resp, err := c.gh.Actions.GetWorkflowRunLogs(ctx, c.owner, c.repo, runID, 10)
	if err != nil {
		return "", fmt.Errorf("failed to get workflow log URL for run %d: %w", runID, err)
	}

	// If we got a URL, fetch the ZIP file from it
	zipURL := url
	if resp != nil && resp.StatusCode != 0 {
		// The go-github library follows redirects automatically, so if we get here
		// with a non-zero status, something went wrong
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusFound {
			return "", fmt.Errorf("failed to get workflow logs: HTTP %d", resp.StatusCode)
		}
	}

	// Fetch the ZIP file
	zipResp, err := c.gh.Client().Get(zipURL.String())
	if err != nil {
		return "", fmt.Errorf("failed to fetch workflow logs for run %d: %w", runID, err)
	}
	defer zipResp.Body.Close()

	if zipResp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to fetch workflow logs: HTTP %d", zipResp.StatusCode)
	}

	// Read the ZIP data
	zipData, err := io.ReadAll(zipResp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read workflow logs for run %d: %w", runID, err)
	}

	// Open the ZIP archive
	zipReader, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
	if err != nil {
		return "", fmt.Errorf("failed to open log archive for run %d: %w", runID, err)
	}

	// Collect all log files and sort them by name for consistent output
	type logFile struct {
		name string
		data string
	}
	var logFiles []logFile

	for _, file := range zipReader.File {
		// Skip directories and files in subdirectories (like __cacache__
		if file.FileInfo().IsDir() {
			continue
		}

		// Read the file content
		rc, err := file.Open()
		if err != nil {
			log.Debugf("Warning: could not open %s in log archive: %v", file.Name, err)
			continue
		}

		content, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			log.Debugf("Warning: could not read %s in log archive: %v", file.Name, err)
			continue
		}

		logFiles = append(logFiles, logFile{
			name: file.Name,
			data: string(content),
		})
	}

	// Sort by filename for consistent output
	sort.Slice(logFiles, func(i, j int) bool {
		return logFiles[i].name < logFiles[j].name
	})

	// Combine all logs into a single string with headers
	var allLogs strings.Builder
	for _, lf := range logFiles {
		// Add a header for each file
		allLogs.WriteString(fmt.Sprintf("=== %s ===\n", lf.name))
		allLogs.WriteString(lf.data)
		// Add newline if the file doesn't end with one
		if !strings.HasSuffix(lf.data, "\n") {
			allLogs.WriteString("\n")
		}
	}

	logStr := strings.TrimRight(allLogs.String(), "\n")

	// Apply line limiting
	if tail > 0 {
		lines := strings.Split(logStr, "\n")
		if len(lines) > tail {
			lines = lines[len(lines)-tail:]
			logStr = strings.Join(lines, "\n") + "\n"
		} else {
			logStr = logStr + "\n"
		}
	} else if head > 0 {
		lines := strings.Split(logStr, "\n")
		if len(lines) > head {
			lines = lines[:head]
			logStr = strings.Join(lines, "\n") + "\n"
		} else {
			logStr = logStr + "\n"
		}
	} else {
		logStr = logStr + "\n"
	}

	return logStr, nil
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

func (c *Client) GetRepoInfo() (string, string) {
	return c.owner, c.repo
}

// InferRepoFromOrigin attempts to extract owner/repo from a git remote URL
func InferRepoFromOrigin(remoteURL string) (owner, repo string, err error) {
	// Handle SSH format: git@github.com:owner/repo.git
	// Also handles malformed URLs like git@github.com:/owner/repo.git (extra slash)
	if strings.Contains(remoteURL, "git@") {
		parts := strings.Split(remoteURL, ":")
		if len(parts) > 1 {
			path := strings.TrimSuffix(parts[1], ".git")
			path = strings.TrimPrefix(path, "/") // Handle extra leading slash
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
