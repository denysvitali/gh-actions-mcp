package github

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/google/go-github/v69/github"
	"github.com/sirupsen/logrus"
)

var log *logrus.Logger

func SetLogger(l *logrus.Logger) {
	log = l
}

type Client struct {
	owner        string
	repo         string
	gh           *github.Client
	perPageLimit int
}

func NewClient(token, owner, repo string) *Client {
	return NewClientWithPerPage(token, owner, repo, 50)
}

// NewClientWithPerPage creates a new GitHub client with a custom per-page limit
func NewClientWithPerPage(token, owner, repo string, perPageLimit int) *Client {
	if perPageLimit <= 0 {
		perPageLimit = 50 // sensible default
	}
	hc := &http.Client{}
	gh := github.NewClient(hc)
	gh = gh.WithAuthToken(token)
	return &Client{
		owner:        owner,
		repo:         repo,
		gh:           gh,
		perPageLimit: perPageLimit,
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

// GetCurrentBranch attempts to detect the current git branch from the working directory.
// Returns empty string if not in a git repository, in detached HEAD state, or on error.
func GetCurrentBranch() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("failed to get working directory: %w", err)
	}

	repo, err := git.PlainOpen(wd)
	if err != nil {
		return "", fmt.Errorf("not in a git repository: %w", err)
	}

	head, err := repo.Head()
	if err != nil {
		return "", fmt.Errorf("failed to get HEAD: %w", err)
	}

	if !head.Name().IsBranch() {
		log.Warnf("HEAD is detached (not on a branch)")
		return "", nil
	}

	return string(head.Name().Short()), nil
}

// CommitInfo contains information about a git commit
type CommitInfo struct {
	SHA    string `json:"sha"`
	Author string `json:"author"`
	Date   string `json:"date"`
	Msg    string `json:"message"`
}

// GetLastCommit returns information about the current HEAD commit.
// Returns nil if not in a git repository or on error.
func GetLastCommit() (*CommitInfo, error) {
	wd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("failed to get working directory: %w", err)
	}

	repo, err := git.PlainOpen(wd)
	if err != nil {
		return nil, fmt.Errorf("not in a git repository: %w", err)
	}

	head, err := repo.Head()
	if err != nil {
		return nil, fmt.Errorf("failed to get HEAD: %w", err)
	}

	commit, err := repo.CommitObject(head.Hash())
	if err != nil {
		return nil, fmt.Errorf("failed to get commit object: %w", err)
	}

	return &CommitInfo{
		SHA:    head.Hash().String()[:7],
		Author: commit.Author.Name,
		Date:   commit.Author.When.Format("2006-01-02 15:04:05"),
		Msg:    strings.SplitN(commit.Message, "\n", 2)[0], // First line only
	}, nil
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

// LogFilterOptions contains parameters for filtering log output
type LogFilterOptions struct {
	Filter       string // Case-insensitive substring match
	FilterRegex  string // Regular expression pattern
	ContextLines int    // Lines of context around matches (like grep -C)
}

// logLine represents a line with metadata for filtering
type logLine struct {
	content     string
	isHeader    bool   // True for "=== filename ===" lines
	fileSection string // The current file section this line belongs to
}

// Pre-compiled regex for detecting file headers
var headerPattern = regexp.MustCompile(`^=== .+ ===$`)

// parseLogLines converts raw log string into structured logLine slice
func parseLogLines(logStr string) []logLine {
	rawLines := strings.Split(logStr, "\n")
	result := make([]logLine, 0, len(rawLines))

	currentFileSection := ""

	for _, raw := range rawLines {
		isHeader := headerPattern.MatchString(raw)
		if isHeader {
			currentFileSection = raw
		}

		result = append(result, logLine{
			content:     raw,
			isHeader:    isHeader,
			fileSection: currentFileSection,
		})
	}

	return result
}

// filterLogLines applies filter/regex matching with context to parsed log lines
func filterLogLines(lines []logLine, opts *LogFilterOptions) ([]logLine, error) {
	if opts == nil || (opts.Filter == "" && opts.FilterRegex == "") {
		return lines, nil
	}

	var matcher func(string) bool

	if opts.FilterRegex != "" {
		re, err := regexp.Compile(opts.FilterRegex)
		if err != nil {
			return nil, fmt.Errorf("invalid regex pattern %q: %w", opts.FilterRegex, err)
		}
		matcher = func(s string) bool {
			return re.MatchString(s)
		}
	} else {
		lowerFilter := strings.ToLower(opts.Filter)
		matcher = func(s string) bool {
			return strings.Contains(strings.ToLower(s), lowerFilter)
		}
	}

	// First pass: find all matching lines (excluding headers)
	matchedIndices := make(map[int]bool)
	for i, line := range lines {
		if !line.isHeader && matcher(line.content) {
			matchedIndices[i] = true
		}
	}

	if len(matchedIndices) == 0 {
		return nil, nil // No matches - return nil to indicate empty result
	}

	// Second pass: expand context, respecting file boundaries
	includedIndices := make(map[int]bool)

	for matchIdx := range matchedIndices {
		matchFileSection := lines[matchIdx].fileSection

		// Add context before (but not crossing file boundaries)
		for i := matchIdx - opts.ContextLines; i < matchIdx; i++ {
			if i >= 0 && !lines[i].isHeader && lines[i].fileSection == matchFileSection {
				includedIndices[i] = true
			}
		}

		// Add the match itself
		includedIndices[matchIdx] = true

		// Add context after (but not crossing file boundaries)
		for i := matchIdx + 1; i <= matchIdx+opts.ContextLines && i < len(lines); i++ {
			if lines[i].isHeader || lines[i].fileSection != matchFileSection {
				break // Stop at file boundary
			}
			includedIndices[i] = true
		}
	}

	// Third pass: build result with necessary headers
	var result []logLine
	var lastFileSection string

	for i, line := range lines {
		if includedIndices[i] {
			// If entering a new file section, include the header
			if line.fileSection != lastFileSection && line.fileSection != "" {
				// Find and include the header for this section
				for j := i; j >= 0; j-- {
					if lines[j].isHeader && lines[j].content == line.fileSection {
						result = append(result, lines[j])
						break
					}
				}
				lastFileSection = line.fileSection
			}
			result = append(result, line)
		}
	}

	return result, nil
}

// linesToString converts logLine slice back to string
func linesToString(lines []logLine) string {
	if len(lines) == 0 {
		return ""
	}

	var sb strings.Builder
	for i, line := range lines {
		sb.WriteString(line.content)
		if i < len(lines)-1 {
			sb.WriteString("\n")
		}
	}
	return sb.String()
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

func (c *Client) GetWorkflows(ctx context.Context) ([]*Workflow, error) {
	workflows, _, err := c.gh.Actions.ListWorkflows(ctx, c.owner, c.repo, &github.ListOptions{PerPage: c.perPageLimit})
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
	// Use the shared helper to resolve workflow ID
	id, _, err := c.ResolveWorkflowID(ctx, workflowID)
	if err != nil {
		return fmt.Errorf("failed to trigger workflow %s: %w", workflowID, err)
	}

	_, err = c.gh.Actions.CreateWorkflowDispatchEventByID(ctx, c.owner, c.repo, id, github.CreateWorkflowDispatchEventRequest{
		Ref: ref,
	})
	if err != nil {
		return fmt.Errorf("failed to trigger workflow %s: %w", workflowID, err)
	}
	return nil
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
// The logs can be filtered by substring or regex pattern, with optional context lines.
// After filtering, results can be limited by line count using head or tail parameters.
// If both head and tail are specified, tail takes precedence.
// If noHeaders is true, file headers (=== filename ===) are not included.
func (c *Client) GetWorkflowLogs(ctx context.Context, runID int64, head, tail int, noHeaders bool, filterOpts *LogFilterOptions) (string, error) {
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

	// Combine all logs into a single string with optional headers
	var allLogs strings.Builder
	for _, lf := range logFiles {
		// Add a header for each file unless noHeaders is true
		if !noHeaders {
			allLogs.WriteString(fmt.Sprintf("=== %s ===\n", lf.name))
		}
		allLogs.WriteString(lf.data)
		// Add newline if the file doesn't end with one
		if !strings.HasSuffix(lf.data, "\n") {
			allLogs.WriteString("\n")
		}
	}

	logStr := strings.TrimRight(allLogs.String(), "\n")

	// Apply filtering if specified
	if filterOpts != nil && (filterOpts.Filter != "" || filterOpts.FilterRegex != "") {
		parsedLines := parseLogLines(logStr)
		filteredLines, err := filterLogLines(parsedLines, filterOpts)
		if err != nil {
			return "", err
		}
		if filteredLines == nil {
			return "", nil // No matches - return empty string
		}
		logStr = linesToString(filteredLines)
	}

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
	// Handle bare owner/repo format (e.g., "owner/repo")
	if !strings.Contains(remoteURL, "://") && !strings.Contains(remoteURL, "@") {
		path := strings.TrimSuffix(remoteURL, ".git")
		repoParts := strings.Split(path, "/")
		if len(repoParts) == 2 {
			return repoParts[0], repoParts[1], nil
		}
	}

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

// ResolveWorkflowID resolves a workflow identifier (ID or name) to a numeric ID and name.
// Returns the workflow ID, name, and an error if the workflow is not found.
func (c *Client) ResolveWorkflowID(ctx context.Context, workflowID string) (int64, string, error) {
	// Try to parse as ID first
	if id, err := ParseWorkflowID(workflowID); err == nil {
		// Look up the workflow to get its name
		workflows, _, err := c.gh.Actions.ListWorkflows(ctx, c.owner, c.repo, &github.ListOptions{PerPage: c.perPageLimit})
		if err != nil {
			return 0, "", fmt.Errorf("failed to list workflows: %w", err)
		}
		for _, w := range workflows.Workflows {
			if w.GetID() == id {
				return id, w.GetName(), nil
			}
		}
		// ID exists but workflow not found - return ID as name
		return id, workflowID, nil
	}

	// Try by name - list workflows and find by name
	workflows, _, err := c.gh.Actions.ListWorkflows(ctx, c.owner, c.repo, &github.ListOptions{PerPage: c.perPageLimit})
	if err != nil {
		return 0, "", fmt.Errorf("failed to list workflows: %w", err)
	}

	for _, w := range workflows.Workflows {
		if w.GetName() == workflowID || w.GetPath() == workflowID {
			return w.GetID(), w.GetName(), nil
		}
	}

	return 0, "", fmt.Errorf("workflow %s not found", workflowID)
}

// ParseWorkflowID parses a workflow ID string into an int64
func ParseWorkflowID(id string) (int64, error) {
	return strconv.ParseInt(id, 10, 64)
}
