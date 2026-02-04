package github

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/google/go-github/v69/github"
	"github.com/sirupsen/logrus"
)

var log *logrus.Logger

func SetLogger(l *logrus.Logger) {
	log = l
}

// HTTPError represents an HTTP error with a status code
type HTTPError struct {
	StatusCode int
	Message    string
}

func (e *HTTPError) Error() string {
	return e.Message
}

// IsHTTPError checks if an error is an HTTPError with a specific status code
func IsHTTPError(err error, statusCode int) bool {
	if err == nil {
		return false
	}
	if httpErr, ok := err.(*HTTPError); ok {
		return httpErr.StatusCode == statusCode
	}
	// Check for go-github error format: "unexpected status code: 404 Not Found"
	// This regex specifically matches the status code pattern at the end of the message
	// preceded by "status code: " to avoid matching repo names like "401k"
	re := regexp.MustCompile(`status code:\s*(\d+)`)
	matches := re.FindStringSubmatch(err.Error())
	if len(matches) > 1 {
		code, _ := strconv.Atoi(matches[1])
		return code == statusCode
	}
	return false
}

// newHTTPError creates an HTTPError from a response and error
func newHTTPError(resp *http.Response, msg string) error {
	statusCode := 0
	if resp != nil {
		statusCode = resp.StatusCode
	}
	return &HTTPError{
		StatusCode: statusCode,
		Message:    fmt.Sprintf("%s: HTTP %d", msg, statusCode),
	}
}

// newHTTPErrorFromGitHub creates an HTTPError from a github.Response
func newHTTPErrorFromGitHub(resp *github.Response, msg string) error {
	statusCode := 0
	if resp != nil {
		statusCode = resp.StatusCode
	}
	return &HTTPError{
		StatusCode: statusCode,
		Message:    fmt.Sprintf("%s: HTTP %d", msg, statusCode),
	}
}

// logSizeThreshold is the size at which we switch to temp file processing
const logSizeThreshold = 10 * 1024 * 1024 // 10MB

// maxLogFileSize is the maximum size for individual log files we'll read
const maxLogFileSize = 50 * 1024 * 1024 // 50MB per file

// regexCache caches compiled regex patterns
var (
	regexCache      = make(map[string]*regexp.Regexp)
	regexCacheMutex sync.RWMutex
)

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

// WorkflowRunMinimal is a compact workflow run representation for reduced token usage
type WorkflowRunMinimal struct {
	ID         int64  `json:"i"`
	Name       string `json:"n"`
	Status     string `json:"s"`
	Conclusion string `json:"c,omitempty"`
	CreatedAt  string `json:"t"`
}

// WorkflowRunCompact extends Minimal with additional fields
type WorkflowRunCompact struct {
	WorkflowRunMinimal
	Branch string `json:"b,omitempty"`
	SHA    string `json:"h,omitempty"`
	Event  string `json:"e,omitempty"`
	Actor  string `json:"a,omitempty"`
	URL    string `json:"u,omitempty"`
}

// WorkflowRunFull is the complete workflow run representation
type WorkflowRunFull struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	Status       string `json:"status"`
	Conclusion   string `json:"conclusion"`
	Branch       string `json:"branch"`
	Event        string `json:"event"`
	Actor        string `json:"actor"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
	URL          string `json:"url"`
	RunNumber    int    `json:"run_number"`
	WorkflowID   int64  `json:"workflow_id"`
	HeadSHA      string `json:"head_sha"`
	StartedAt    string `json:"started_at,omitempty"`
	CompletedAt  string `json:"completed_at,omitempty"`
}

// Job represents a workflow run job
type Job struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	Status       string `json:"status"`
	Conclusion   string `json:"conclusion,omitempty"`
	StartedAt    string `json:"started_at,omitempty"`
	CompletedAt  string `json:"completed_at,omitempty"`
	RunnerName   string `json:"runner_name,omitempty"`
	RunnerGroup  string `json:"runner_group,omitempty"`
	Labels       []string `json:"labels,omitempty"`
	WorkflowRunID int64  `json:"workflow_run_id"`
}

// Artifact represents a workflow run artifact
type Artifact struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	SizeInBytes  int64  `json:"size_in_bytes"`
	CreatedAt    string `json:"created_at"`
	ExpiresAt    string `json:"expires_at,omitempty"`
	ArchiveURL   string `json:"archive_url,omitempty"`
}

// ArtifactFile represents a single file within an artifact
type ArtifactFile struct {
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	Content  string `json:"content,omitempty"`
	Encoding string `json:"encoding,omitempty"` // "text" or "base64"
}

// ArtifactContent represents the contents of an artifact
type ArtifactContent struct {
	Name        string          `json:"name"`
	ID          int64           `json:"id"`
	SizeInBytes int64           `json:"size_in_bytes"`
	Files       []*ArtifactFile `json:"files"`
	FileCount   int             `json:"file_count"`
}

// ArtifactDownloadResult represents the result of downloading an artifact
type ArtifactDownloadResult struct {
	Name        string `json:"name"`
	ID          int64  `json:"id"`
	SavedPath   string `json:"saved_path"`
	FileCount   int    `json:"file_count"`
	TotalSize   int64  `json:"total_size"`
}

// LogFileInfo represents information about a single log file in the archive
type LogFileInfo struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// CheckRun represents a GitHub check run
type CheckRun struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	Conclusion  string `json:"conclusion,omitempty"`
	StartedAt   string `json:"started_at,omitempty"`
	CompletedAt string `json:"completed_at,omitempty"`
	AppName     string `json:"app_name,omitempty"`
	DetailsURL  string `json:"details_url,omitempty"`
}

// CombinedCheckStatus represents the combined status of all check runs for a commit
type CombinedCheckStatus struct {
	SHA          string      `json:"sha"`
	State        string      `json:"state"` // "pending", "success", "failure", "neutral"
	TotalCount   int         `json:"total_count"`
	CheckRuns    []*CheckRun `json:"check_runs"`
	ByConclusion map[string]int `json:"by_conclusion"`
}

// WaitRunResult is the result of waiting for a workflow run
type WaitRunResult struct {
	Status           string  `json:"status"`           // "completed", "timed_out"
	Conclusion       string  `json:"conclusion,omitempty"` // "success", "failure", etc.
	DurationSeconds  float64 `json:"duration_seconds"`
	RunURL           string  `json:"run_url"`
	StartedAt        string  `json:"started_at,omitempty"`
	CompletedAt      string  `json:"completed_at,omitempty"`
	TimeoutReached   bool    `json:"timeout_reached"`
	PollCount        int     `json:"poll_count"`
}

// WaitCommitChecksResult is the result of waiting for commit checks
type WaitCommitChecksResult struct {
	OverallConclusion string  `json:"overall_conclusion"` // "success", "failure", "pending", "neutral"
	ChecksTotal       int     `json:"checks_total"`
	ChecksByConclusion map[string]int `json:"checks_by_conclusion"`
	DurationSeconds   float64 `json:"duration_seconds"`
	TimeoutReached    bool    `json:"timeout_reached"`
}

// ManageRunAction represents an action to take on a workflow run
type ManageRunAction string

const (
	ManageRunActionCancel        ManageRunAction = "cancel"
	ManageRunActionRerun         ManageRunAction = "rerun"
	ManageRunActionRerunFailed   ManageRunAction = "rerun_failed"
)

// ManageRunResult is the result of managing a workflow run
type ManageRunResult struct {
	RunID   int64            `json:"run_id"`
	Action  ManageRunAction  `json:"action"`
	Status  string           `json:"status"` // "success", "failed"
	Message string           `json:"message,omitempty"`
}

// ListRunsOptions contains parameters for listing workflow runs
type ListRunsOptions struct {
	WorkflowID  *int64  // Optional: filter by workflow ID
	Branch      string  // Optional: filter by branch
	Status      string  // Optional: queued, in_progress, completed, etc.
	Conclusion  string  // Optional: success, failure, neutral, cancelled, etc.
	Per_page    int     // Optional: number of results per page
	CreatedAfter string // Optional: ISO 8601 date string
	Event       string  // Optional: push, pull_request, etc.
	Actor       string  // Optional: GitHub username
}

// GetCheckRunsOptions contains parameters for getting check runs
type GetCheckRunsOptions struct {
	CheckName string // Optional: filter by check name
	Status    string // Optional: queued, in_progress, completed
	Filter    string // Optional: "latest" (default) or "all"
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

// getCachedRegex returns a cached compiled regex or compiles and caches a new one
func getCachedRegex(pattern string) (*regexp.Regexp, error) {
	regexCacheMutex.RLock()
	re, ok := regexCache[pattern]
	regexCacheMutex.RUnlock()

	if ok {
		return re, nil
	}

	regexCacheMutex.Lock()
	defer regexCacheMutex.Unlock()

	// Double-check after acquiring write lock
	re, ok = regexCache[pattern]
	if ok {
		return re, nil
	}

	compiled, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}

	regexCache[pattern] = compiled
	return compiled, nil
}

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
	var matcherErr error

	if opts.FilterRegex != "" {
		re, err := getCachedRegex(opts.FilterRegex)
		if err != nil {
			return nil, fmt.Errorf("invalid regex pattern %q: %w", opts.FilterRegex, err)
		}
		matcher = func(s string) bool {
			return re.MatchString(s)
		}
		matcherErr = err
	} else {
		lowerFilter := strings.ToLower(opts.Filter)
		matcher = func(s string) bool {
			return strings.Contains(strings.ToLower(s), lowerFilter)
		}
	}

	// Check for matcher compilation errors
	if matcherErr != nil {
		return nil, matcherErr
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

// logFile represents a log file's name and content
type logFile struct {
	name string
	data string
}

// readZipArchive reads a ZIP archive from a URL, using temp files for large downloads
// to avoid loading everything into memory. Returns a slice of log files.
func readZipArchive(zipURL string, httpClient *http.Client) ([]logFile, int64, error) {
	// Fetch the ZIP file
	zipResp, err := httpClient.Get(zipURL)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to fetch ZIP: %w", err)
	}
	defer zipResp.Body.Close()

	if zipResp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("failed to fetch ZIP: HTTP %d", zipResp.StatusCode)
	}

	// Check content length to decide approach
	contentLength := zipResp.ContentLength
	useTempFile := contentLength > logSizeThreshold || contentLength < 0

	var zipReader *zip.Reader
	var cleanup func()

	if useTempFile {
		// For large archives or unknown size, use a temp file
		tempFile, err := os.CreateTemp("", "logs-*.zip")
		if err != nil {
			return nil, 0, fmt.Errorf("failed to create temp file: %w", err)
		}

		// Copy to temp file
		written, err := io.Copy(tempFile, zipResp.Body)
		if err != nil {
			tempFile.Close()
			os.Remove(tempFile.Name())
			return nil, 0, fmt.Errorf("failed to write to temp file: %w", err)
		}

		// Re-open for reading
		_, err = tempFile.Seek(0, 0)
		if err != nil {
			tempFile.Close()
			os.Remove(tempFile.Name())
			return nil, 0, fmt.Errorf("failed to seek temp file: %w", err)
		}

		zipReader, err = zip.NewReader(tempFile, written)
		if err != nil {
			tempFile.Close()
			os.Remove(tempFile.Name())
			return nil, 0, fmt.Errorf("failed to open ZIP: %w", err)
		}

		// Set up cleanup function
		cleanup = func() {
			tempFile.Close()
			os.Remove(tempFile.Name())
		}

		contentLength = written
	} else {
		// For small archives, read into memory
		zipData, err := io.ReadAll(zipResp.Body)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to read ZIP: %w", err)
		}

		zipReader, err = zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
		if err != nil {
			return nil, 0, fmt.Errorf("failed to open ZIP: %w", err)
		}

		contentLength = int64(len(zipData))
		cleanup = func() {}
	}

	// Process files
	var logFiles []logFile
	for _, file := range zipReader.File {
		if file.FileInfo().IsDir() {
			continue
		}

		// Skip excessively large individual files
		if file.UncompressedSize64 > uint64(maxLogFileSize) {
			log.Debugf("Skipping large log file %s (%d bytes)", file.Name, file.UncompressedSize64)
			continue
		}

		rc, err := file.Open()
		if err != nil {
			log.Debugf("Warning: could not open %s in ZIP: %v", file.Name, err)
			continue
		}

		// Use limited reader to prevent excessive memory usage
		content, err := io.ReadAll(io.LimitReader(rc, maxLogFileSize))
		rc.Close()
		if err != nil {
			log.Debugf("Warning: could not read %s in ZIP: %v", file.Name, err)
			continue
		}

		logFiles = append(logFiles, logFile{
			name: file.Name,
			data: string(content),
		})
	}

	// Clean up temp file if used
	cleanup()

	// Sort by filename for consistent output
	sort.Slice(logFiles, func(i, j int) bool {
		return logFiles[i].name < logFiles[j].name
	})

	return logFiles, contentLength, nil
}

// GetWorkflowLogFiles returns a list of log files available in the workflow run archive
func (c *Client) GetWorkflowLogFiles(ctx context.Context, runID int64) ([]*LogFileInfo, error) {
	// Get the log archive URL
	url, resp, err := c.gh.Actions.GetWorkflowRunLogs(ctx, c.owner, c.repo, runID, 10)
	if err != nil {
		return nil, fmt.Errorf("failed to get workflow log URL for run %d: %w", runID, err)
	}

	if resp != nil && resp.StatusCode != 0 {
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusFound {
			return nil, newHTTPErrorFromGitHub(resp, "failed to get workflow logs")
		}
	}

	// Fetch ZIP archive
	logFiles, _, err := readZipArchive(url.String(), c.gh.Client())
	if err != nil {
		return nil, fmt.Errorf("failed to read log archive for run %d: %w", runID, err)
	}

	// Convert to LogFileInfo
	result := make([]*LogFileInfo, 0, len(logFiles))
	for _, lf := range logFiles {
		result = append(result, &LogFileInfo{
			Path: lf.name,
			Size: int64(len(lf.data)),
		})
	}

	return result, nil
}

// GetWorkflowLogsWithPattern retrieves logs for a workflow run with optional file pattern filtering
func (c *Client) GetWorkflowLogsWithPattern(ctx context.Context, runID int64, head, tail, offset int, noHeaders bool, filePattern string, filterOpts *LogFilterOptions) (string, error) {
	// Get the log archive URL
	url, resp, err := c.gh.Actions.GetWorkflowRunLogs(ctx, c.owner, c.repo, runID, 10)
	if err != nil {
		return "", fmt.Errorf("failed to get workflow log URL for run %d: %w", runID, err)
	}

	if resp != nil && resp.StatusCode != 0 {
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusFound {
			return "", newHTTPErrorFromGitHub(resp, "failed to get workflow logs")
		}
	}

	// Read ZIP archive
	logFiles, _, err := readZipArchive(url.String(), c.gh.Client())
	if err != nil {
		return "", fmt.Errorf("failed to read log archive for run %d: %w", runID, err)
	}

	// Apply file pattern filter if specified
	if filePattern != "" {
		filtered := make([]logFile, 0)
		for _, lf := range logFiles {
			matched, err := filepath.Match(filePattern, lf.name)
			if err != nil {
				return "", fmt.Errorf("invalid file pattern %q: %w", filePattern, err)
			}
			if matched {
				filtered = append(filtered, lf)
			}
		}
		logFiles = filtered
	}

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

	// Apply line limiting (offset, head, tail)
	lines := strings.Split(logStr, "\n")

	// Apply offset first (skip lines from the beginning)
	if offset > 0 && offset < len(lines) {
		lines = lines[offset:]
	}

	// Apply tail (last N lines - takes precedence over head)
	if tail > 0 {
		if len(lines) > tail {
			lines = lines[len(lines)-tail:]
		}
	} else if head > 0 {
		// Apply head (at most N lines)
		if len(lines) > head {
			lines = lines[:head]
		}
	}

	logStr = strings.Join(lines, "\n")
	if logStr != "" {
		logStr = logStr + "\n"
	}

	return logStr, nil
}

// GetWorkflowLogs retrieves the logs for a workflow run and returns them as a string.
// The logs can be filtered by substring or regex pattern, with optional context lines.
// After filtering, results can be limited by line count using head, tail, and offset parameters.
// - offset: skip first N lines (0-based)
// - head: return at most N lines from the offset (if specified)
// - tail: return the last N lines (takes precedence over head+offset)
// If noHeaders is true, file headers (=== filename ===) are not included.
func (c *Client) GetWorkflowLogs(ctx context.Context, runID int64, head, tail, offset int, noHeaders bool, filterOpts *LogFilterOptions) (string, error) {
	return c.GetWorkflowLogsWithPattern(ctx, runID, head, tail, offset, noHeaders, "", filterOpts)
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

// ListRepositoryWorkflowRunsWithOptions lists workflow runs with comprehensive filtering options
func (c *Client) ListRepositoryWorkflowRunsWithOptions(ctx context.Context, opts *ListRunsOptions) ([]*WorkflowRun, error) {
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

	var runs *github.WorkflowRuns
	var err error

	if opts.WorkflowID != nil {
		// List runs for a specific workflow
		runs, _, err = c.gh.Actions.ListWorkflowRunsByID(ctx, c.owner, c.repo, *opts.WorkflowID, githubOpts)
	} else {
		// List all repository workflow runs
		runs, _, err = c.gh.Actions.ListRepositoryWorkflowRuns(ctx, c.owner, c.repo, githubOpts)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to list workflow runs: %w", err)
	}

	result := make([]*WorkflowRun, 0, len(runs.WorkflowRuns))
	for _, run := range runs.WorkflowRuns {
		// Apply conclusion filter client-side if needed
		if opts.Conclusion != "" && run.GetConclusion() != opts.Conclusion {
			continue
		}
		result = append(result, workflowRunFromGitHub(run))
	}

	return result, nil
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

		result = append(result, &Job{
			ID:            job.GetID(),
			Name:          job.GetName(),
			Status:        job.GetStatus(),
			Conclusion:    job.GetConclusion(),
			StartedAt:     formatTime(job.StartedAt),
			CompletedAt:   formatTime(job.CompletedAt),
			RunnerName:    job.GetRunnerName(),
			RunnerGroup:   job.GetRunnerGroupName(),
			Labels:        labels,
			WorkflowRunID: job.GetRunID(),
		})
	}

	return result, nil
}

// GetWorkflowJobLogs retrieves logs for a specific job
func (c *Client) GetWorkflowJobLogs(ctx context.Context, jobID int64, head, tail, offset int, noHeaders bool, filterOpts *LogFilterOptions) (string, error) {
	// Get the log archive
	url, resp, err := c.gh.Actions.GetWorkflowJobLogs(ctx, c.owner, c.repo, jobID, 10)
	if err != nil {
		return "", fmt.Errorf("failed to get job log URL for job %d: %w", jobID, err)
	}

	// Check response status
	if resp != nil && resp.StatusCode != 0 {
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusFound {
			return "", newHTTPErrorFromGitHub(resp, "failed to get job logs")
		}
	}

	// Fetch the ZIP file
	zipResp, err := c.gh.Client().Get(url.String())
	if err != nil {
		return "", fmt.Errorf("failed to fetch job logs for job %d: %w", jobID, err)
	}
	defer zipResp.Body.Close()

	if zipResp.StatusCode != http.StatusOK {
		return "", &HTTPError{StatusCode: zipResp.StatusCode, Message: fmt.Sprintf("failed to fetch job logs: HTTP %d", zipResp.StatusCode)}
	}

	// Read the ZIP data
	zipData, err := io.ReadAll(zipResp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read job logs for job %d: %w", jobID, err)
	}

	// Open the ZIP archive
	zipReader, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
	if err != nil {
		return "", fmt.Errorf("failed to open log archive for job %d: %w", jobID, err)
	}

	// Collect all log files (inline struct)
	type logFile struct {
		name string
		data string
	}
	var logFiles []logFile

	for _, file := range zipReader.File {
		if file.FileInfo().IsDir() {
			continue
		}

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

	// Sort by filename
	sort.Slice(logFiles, func(i, j int) bool {
		return logFiles[i].name < logFiles[j].name
	})

	// Combine all logs
	var allLogs strings.Builder
	for _, lf := range logFiles {
		if !noHeaders {
			allLogs.WriteString(fmt.Sprintf("=== %s ===\n", lf.name))
		}
		allLogs.WriteString(lf.data)
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
			return "", nil
		}
		logStr = linesToString(filteredLines)
	}

	// Apply line limiting (offset, head, tail)
	lines := strings.Split(logStr, "\n")

	// Apply offset first (skip lines from the beginning)
	if offset > 0 && offset < len(lines) {
		lines = lines[offset:]
	}

	// Apply tail (last N lines - takes precedence over head)
	if tail > 0 {
		if len(lines) > tail {
			lines = lines[len(lines)-tail:]
		}
	} else if head > 0 {
		// Apply head (at most N lines)
		if len(lines) > head {
			lines = lines[:head]
		}
	}

	logStr = strings.Join(lines, "\n")
	if logStr != "" {
		logStr = logStr + "\n"
	}

	return logStr, nil
}

// GetWorkflowRunArtifacts retrieves artifacts for a workflow run
func (c *Client) GetWorkflowRunArtifacts(ctx context.Context, runID int64) ([]*Artifact, error) {
	arts, _, err := c.gh.Actions.ListWorkflowRunArtifacts(ctx, c.owner, c.repo, runID, &github.ListOptions{
		PerPage: c.perPageLimit,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list artifacts for run %d: %w", runID, err)
	}

	result := make([]*Artifact, 0, len(arts.Artifacts))
	for _, art := range arts.Artifacts {
		result = append(result, &Artifact{
			ID:          art.GetID(),
			Name:        art.GetName(),
			SizeInBytes: art.GetSizeInBytes(),
			CreatedAt:   formatTimeValue(art.GetCreatedAt()),
			ExpiresAt:   formatTimeValue(art.GetExpiresAt()),
			ArchiveURL:  art.GetArchiveDownloadURL(),
		})
	}

	return result, nil
}

// GetArtifactByID retrieves a single artifact by its ID
func (c *Client) GetArtifactByID(ctx context.Context, artifactID int64) (*Artifact, error) {
	art, _, err := c.gh.Actions.GetArtifact(ctx, c.owner, c.repo, artifactID)
	if err != nil {
		return nil, fmt.Errorf("failed to get artifact %d: %w", artifactID, err)
	}

	return &Artifact{
		ID:          art.GetID(),
		Name:        art.GetName(),
		SizeInBytes: art.GetSizeInBytes(),
		CreatedAt:   formatTimeValue(art.GetCreatedAt()),
		ExpiresAt:   formatTimeValue(art.GetExpiresAt()),
		ArchiveURL:  art.GetArchiveDownloadURL(),
	}, nil
}

// GetArtifactContent retrieves the contents of an artifact without downloading to disk
// If filePattern is provided, only files matching the pattern will be returned
// maxFileSize limits the size of individual files read (in bytes, 0 for unlimited)
// For text files, content is returned as a string. For binary files, content is base64 encoded.
func (c *Client) GetArtifactContent(ctx context.Context, artifactID int64, filePattern string, maxFileSize int64) (*ArtifactContent, error) {
	// First get artifact metadata
	artifact, err := c.GetArtifactByID(ctx, artifactID)
	if err != nil {
		return nil, err
	}

	// Download the artifact ZIP
	zipURL, resp, err := c.gh.Actions.DownloadArtifact(ctx, c.owner, c.repo, artifactID, 10)
	if err != nil {
		return nil, fmt.Errorf("failed to get artifact download URL: %w", err)
	}

	if resp != nil && resp.StatusCode != 0 {
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusFound {
			return nil, fmt.Errorf("failed to download artifact: HTTP %d", resp.StatusCode)
		}
	}

	// Fetch the ZIP file
	zipResp, err := c.gh.Client().Get(zipURL.String())
	if err != nil {
		return nil, fmt.Errorf("failed to fetch artifact: %w", err)
	}
	defer zipResp.Body.Close()

	if zipResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch artifact: HTTP %d", zipResp.StatusCode)
	}

	// Read the ZIP data
	zipData, err := io.ReadAll(zipResp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read artifact data: %w", err)
	}

	// Open the ZIP archive
	zipReader, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
	if err != nil {
		return nil, fmt.Errorf("failed to open artifact archive: %w", err)
	}

	// Process files in the ZIP
	var files []*ArtifactFile
	var totalSize int64

	for _, file := range zipReader.File {
		if file.FileInfo().IsDir() {
			continue
		}

		// Skip files larger than maxFileSize (if specified)
		if maxFileSize > 0 && file.UncompressedSize64 > uint64(maxFileSize) {
			files = append(files, &ArtifactFile{
				Path:    file.Name,
				Size:    int64(file.UncompressedSize64),
				Content: fmt.Sprintf("(file too large to read, size: %d bytes)", file.UncompressedSize64),
			})
			totalSize += int64(file.UncompressedSize64)
			continue
		}

		// Apply file pattern filter if specified
		if filePattern != "" {
			matched, err := filepath.Match(filePattern, file.Name)
			if err != nil {
				return nil, fmt.Errorf("invalid file pattern %q: %w", filePattern, err)
			}
			if !matched {
				continue
			}
		}

		// Read file content
		rc, err := file.Open()
		if err != nil {
			log.Debugf("Warning: could not open %s in artifact: %v", file.Name, err)
			continue
		}

		content, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			log.Debugf("Warning: could not read %s in artifact: %v", file.Name, err)
			continue
		}

		totalSize += int64(file.UncompressedSize64)

		// Detect if content is text or binary
		encoding := "text"
		contentStr := string(content)
		if !isTextContent(content) {
			encoding = "base64"
			contentStr = base64.StdEncoding.EncodeToString(content)
		}

		files = append(files, &ArtifactFile{
			Path:     file.Name,
			Size:     int64(file.UncompressedSize64),
			Content:  contentStr,
			Encoding: encoding,
		})
	}

	// Sort files by path
	sort.Slice(files, func(i, j int) bool {
		return files[i].Path < files[j].Path
	})

	return &ArtifactContent{
		Name:        artifact.Name,
		ID:          artifact.ID,
		SizeInBytes: artifact.SizeInBytes,
		Files:       files,
		FileCount:   len(files),
	}, nil
}

// DownloadArtifact downloads an artifact and saves it to a file
// If outputPath is empty, a default path will be generated (artifact-name.zip)
func (c *Client) DownloadArtifact(ctx context.Context, artifactID int64, outputPath string) (*ArtifactDownloadResult, error) {
	// First get artifact metadata
	artifact, err := c.GetArtifactByID(ctx, artifactID)
	if err != nil {
		return nil, err
	}

	// Generate default output path if not provided
	if outputPath == "" {
		outputPath = fmt.Sprintf("%s.zip", artifact.Name)
	}

	// Download the artifact ZIP
	zipURL, resp, err := c.gh.Actions.DownloadArtifact(ctx, c.owner, c.repo, artifactID, 10)
	if err != nil {
		return nil, fmt.Errorf("failed to get artifact download URL: %w", err)
	}

	if resp != nil && resp.StatusCode != 0 {
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusFound {
			return nil, fmt.Errorf("failed to download artifact: HTTP %d", resp.StatusCode)
		}
	}

	// Fetch the ZIP file
	zipResp, err := c.gh.Client().Get(zipURL.String())
	if err != nil {
		return nil, fmt.Errorf("failed to fetch artifact: %w", err)
	}
	defer zipResp.Body.Close()

	if zipResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch artifact: HTTP %d", zipResp.StatusCode)
	}

	// Create output file
	outFile, err := os.Create(outputPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create output file %q: %w", outputPath, err)
	}
	defer outFile.Close()

	// Copy data to file
	bytesWritten, err := io.Copy(outFile, zipResp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to write artifact to file: %w", err)
	}

	// Count files in the archive
	outFile.Seek(0, 0)
	zipReader, err := zip.NewReader(outFile, bytesWritten)
	if err != nil {
		return &ArtifactDownloadResult{
			Name:      artifact.Name,
			ID:        artifact.ID,
			SavedPath: outputPath,
			FileCount: 0,
			TotalSize: bytesWritten,
		}, nil
	}

	fileCount := 0
	for _, file := range zipReader.File {
		if !file.FileInfo().IsDir() {
			fileCount++
		}
	}

	log.Infof("Downloaded artifact %q to %s (%d bytes, %d files)", artifact.Name, outputPath, bytesWritten, fileCount)

	return &ArtifactDownloadResult{
		Name:      artifact.Name,
		ID:        artifact.ID,
		SavedPath: outputPath,
		FileCount: fileCount,
		TotalSize: bytesWritten,
	}, nil
}

// isTextContent attempts to detect if content is text or binary
func isTextContent(data []byte) bool {
	if len(data) == 0 {
		return true
	}

	// Check first 512 bytes for null bytes (indicates binary)
	sampleSize := 512
	if len(data) < sampleSize {
		sampleSize = len(data)
	}

	for i := 0; i < sampleSize; i++ {
		if data[i] == 0 {
			return false
		}
	}

	// Check for common text file extensions or content patterns
	return true
}

// GetCheckRunsForRef retrieves check runs for a specific ref (commit SHA, branch, or tag)
func (c *Client) GetCheckRunsForRef(ctx context.Context, ref string, opts *GetCheckRunsOptions) (*CombinedCheckStatus, error) {
	githubOpts := &github.ListCheckRunsOptions{
		ListOptions: github.ListOptions{PerPage: c.perPageLimit},
	}

	if opts != nil {
		if opts.CheckName != "" {
			githubOpts.CheckName = &opts.CheckName
		}
		if opts.Status != "" {
			githubOpts.Status = &opts.Status
		}
		filter := "latest"
		if opts.Filter == "all" {
			filter = "all"
		}
		githubOpts.Filter = &filter
	}

	checkRuns, _, err := c.gh.Checks.ListCheckRunsForRef(ctx, c.owner, c.repo, ref, githubOpts)
	if err != nil {
		return nil, fmt.Errorf("failed to list check runs for ref %s: %w", ref, err)
	}

	result := &CombinedCheckStatus{
		SHA:          ref,
		CheckRuns:    make([]*CheckRun, 0),
		ByConclusion: make(map[string]int),
	}

	// Convert check runs
	for _, cr := range checkRuns.CheckRuns {
		checkRun := &CheckRun{
			ID:          cr.GetID(),
			Name:        cr.GetName(),
			Status:      cr.GetStatus(),
			Conclusion:  cr.GetConclusion(),
			StartedAt:   formatTime(cr.StartedAt),
			CompletedAt: formatTime(cr.CompletedAt),
			AppName:     cr.App.GetName(),
			DetailsURL:  cr.GetDetailsURL(),
		}
		result.CheckRuns = append(result.CheckRuns, checkRun)

		// Count by conclusion
		if cr.GetConclusion() != "" {
			result.ByConclusion[cr.GetConclusion()]++
		} else if cr.GetStatus() != "completed" {
			result.ByConclusion[cr.GetStatus()]++
		}
	}

	result.TotalCount = len(result.CheckRuns)

	// Determine overall state
	result.State = c.determineOverallState(result.CheckRuns)

	return result, nil
}

// determineOverallState determines the overall check status from individual check runs
func (c *Client) determineOverallState(checkRuns []*CheckRun) string {
	if len(checkRuns) == 0 {
		return "pending"
	}

	hasPending := false
	hasFailure := false
	hasSuccess := false

	for _, cr := range checkRuns {
		if cr.Status == "completed" {
			if cr.Conclusion == "failure" || cr.Conclusion == "timed_out" {
				hasFailure = true
			} else if cr.Conclusion == "success" {
				hasSuccess = true
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

// WaitForRun waits for a workflow run to complete (silent polling)
func (c *Client) WaitForRun(ctx context.Context, runID int64, timeoutMinutes int) (*WaitRunResult, error) {
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

		// Check if completed
		if run.Status == "completed" {
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
					OverallConclusion: "timed_out",
					ChecksTotal:       status.TotalCount,
					ChecksByConclusion: byConclusion,
					DurationSeconds:   elapsed.Seconds(),
					TimeoutReached:    true,
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

		// Check if all checks are complete
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

// formatTime formats a github.Timestamp pointer into an ISO string
func formatTime(t *github.Timestamp) string {
	if t == nil {
		return ""
	}
	return t.String()
}

// formatTimeValue formats a github.Timestamp value into an ISO string
func formatTimeValue(t github.Timestamp) string {
	return t.String()
}

// GetLogSection extracts a specific section from logs by header pattern
// Section headers typically look like "##[group]Section Name" or similar patterns
// If jobID is 0, it fetches logs for the run; otherwise for the specific job
func (c *Client) GetLogSection(ctx context.Context, runID, jobID int64, sectionPattern string, filterOpts *LogFilterOptions) (string, error) {
	var logs string
	var err error

	// Fetch logs based on whether we have a job ID
	if jobID > 0 {
		logs, err = c.GetWorkflowJobLogs(ctx, jobID, 0, 0, 0, false, nil)
	} else {
		logs, err = c.GetWorkflowLogs(ctx, runID, 0, 0, 0, false, nil)
	}

	if err != nil {
		return "", err
	}

	// Extract the section
	section, err := extractSection(logs, sectionPattern)
	if err != nil {
		return "", err
	}

	// Apply additional filtering if specified
	if filterOpts != nil && (filterOpts.Filter != "" || filterOpts.FilterRegex != "") {
		parsedLines := parseLogLines(section)
		filteredLines, err := filterLogLines(parsedLines, filterOpts)
		if err != nil {
			return "", err
		}
		if filteredLines == nil {
			return "", nil
		}
		section = linesToString(filteredLines)
	}

	return section, nil
}

// extractSection parses logs and extracts content between section markers
// GitHub Actions logs use ##[group]Section Name and ##[endgroup] markers
func extractSection(logs string, sectionPattern string) (string, error) {
	if sectionPattern == "" {
		return logs, nil
	}

	lines := strings.Split(logs, "\n")
	var result []string
	inSection := false
	sectionDepth := 0

	// Compile regex for matching section headers
	re, err := getCachedRegex(sectionPattern)
	if err != nil {
		return "", fmt.Errorf("invalid section pattern %q: %w", sectionPattern, err)
	}

	for _, line := range lines {
		// Check for group start (various formats)
		// GitHub Actions uses: ##[group]Section Name
		// Also handle: ::group::Section Name
		isGroupStart := strings.Contains(line, "##[group]") || strings.Contains(line, "::group::")
		isGroupEnd := strings.Contains(line, "##[endgroup]") || strings.Contains(line, "::endgroup::")

		if isGroupStart {
			sectionDepth++
			// Check if this is the section we're looking for
			if !inSection && re.MatchString(line) {
				inSection = true
				result = append(result, line)
			}
		} else if isGroupEnd {
			if inSection {
				result = append(result, line)
				sectionDepth--
				if sectionDepth == 0 {
					inSection = false
				}
			} else {
				sectionDepth--
				if sectionDepth < 0 {
					sectionDepth = 0
				}
			}
		} else if inSection {
			result = append(result, line)
		}
	}

	if len(result) == 0 {
		return "", fmt.Errorf("section matching pattern %q not found", sectionPattern)
	}

	return strings.Join(result, "\n"), nil
}

// LogSection represents a section found in workflow logs
type LogSection struct {
	Name     string `json:"name"`
	Line     int    `json:"line"`
	JobName  string `json:"job_name,omitempty"`
}

// ListLogSections extracts all section headers from workflow logs
// Returns a list of sections with their names and line numbers
func (c *Client) ListLogSections(ctx context.Context, runID, jobID int64) ([]*LogSection, error) {
	var logs string
	var err error

	// Fetch logs based on whether we have a job ID
	if jobID > 0 {
		logs, err = c.GetWorkflowJobLogs(ctx, jobID, 0, 0, 0, false, nil)
	} else {
		logs, err = c.GetWorkflowLogs(ctx, runID, 0, 0, 0, false, nil)
	}

	if err != nil {
		return nil, err
	}

	return extractSections(logs), nil
}

// extractSections parses logs and returns all section headers found
// GitHub Actions logs use ##[group]Section Name and ::group::Section Name markers
func extractSections(logs string) []*LogSection {
	lines := strings.Split(logs, "\n")
	var sections []*LogSection
	currentJob := ""

	for i, line := range lines {
		// Check for job header (=== filename ===)
		if strings.HasPrefix(line, "=== ") && strings.HasSuffix(line, " ===") {
			currentJob = strings.TrimPrefix(strings.TrimSuffix(line, " ==="), "=== ")
			continue
		}

		// Check for group start markers
		var sectionName string
		if strings.Contains(line, "##[group]") {
			sectionName = extractSectionName(line, "##[group]")
		} else if strings.Contains(line, "::group::") {
			sectionName = extractSectionName(line, "::group::")
		}

		if sectionName != "" {
			sections = append(sections, &LogSection{
				Name:    sectionName,
				Line:    i + 1, // 1-based line number
				JobName: currentJob,
			})
		}
	}

	return sections
}

// extractSectionName extracts the section name after a marker
func extractSectionName(line, marker string) string {
	idx := strings.Index(line, marker)
	if idx == -1 {
		return ""
	}
	name := line[idx+len(marker):]
	return strings.TrimSpace(name)
}
