package github

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
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

var log = logrus.New()

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

// maxRedirects is the maximum number of HTTP redirects to follow
const maxRedirects = 10

// regexCache caches compiled regex patterns
var (
	regexCache      = make(map[string]*regexp.Regexp)
	regexCacheMutex sync.RWMutex
)

// presignedHTTPClient is used for fetching pre-signed storage URLs (no auth headers)
var presignedHTTPClient = &http.Client{Timeout: 30 * time.Second}

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
	c, _ := NewClientWithOptions(ClientOptions{
		Token:        token,
		Owner:        owner,
		Repo:         repo,
		PerPageLimit: perPageLimit,
	})
	return c
}

// ClientOptions configures a GitHub client. APIBaseURL / UploadURL enable
// routing through a GitHub Enterprise server or a reverse proxy like gh-proxy.
type ClientOptions struct {
	Token        string
	Owner        string
	Repo         string
	PerPageLimit int
	// APIBaseURL overrides the default https://api.github.com/ base URL.
	// Must end with a trailing slash (go-github requirement). Example for
	// gh-proxy: "http://gh-proxy:8080/api/".
	APIBaseURL string
	// UploadURL overrides the upload URL. Defaults to APIBaseURL when empty.
	UploadURL string
}

// NewClientWithOptions creates a new GitHub client using the provided options.
func NewClientWithOptions(opts ClientOptions) (*Client, error) {
	if opts.PerPageLimit <= 0 {
		opts.PerPageLimit = 50
	}
	hc := &http.Client{Timeout: 30 * time.Second}
	gh := github.NewClient(hc).WithAuthToken(opts.Token)
	if opts.APIBaseURL != "" {
		// Set BaseURL directly rather than via WithEnterpriseURLs, which
		// would auto-append "api/v3/" and break non-Enterprise proxies
		// (e.g. gh-proxy, which expects "/api/repos/...").
		base, err := url.Parse(opts.APIBaseURL)
		if err != nil {
			return nil, fmt.Errorf("parse api_base_url: %w", err)
		}
		if !strings.HasSuffix(base.Path, "/") {
			base.Path += "/"
		}
		gh.BaseURL = base
		uploadStr := opts.UploadURL
		if uploadStr == "" {
			uploadStr = opts.APIBaseURL
		}
		upload, err := url.Parse(uploadStr)
		if err != nil {
			return nil, fmt.Errorf("parse upload_url: %w", err)
		}
		if !strings.HasSuffix(upload.Path, "/") {
			upload.Path += "/"
		}
		gh.UploadURL = upload
	}
	return &Client{
		owner:        opts.Owner,
		repo:         opts.Repo,
		gh:           gh,
		perPageLimit: opts.PerPageLimit,
	}, nil
}

type WorkflowRun struct {
	ID              int64   `json:"id"`
	Name            string  `json:"name"`
	Status          string  `json:"status"`
	Conclusion      string  `json:"conclusion"`
	Branch          string  `json:"branch"`
	HeadSHA         string  `json:"head_sha,omitempty"`
	Event           string  `json:"event"`
	Actor           string  `json:"actor"`
	CreatedAt       string  `json:"created_at"`
	UpdatedAt       string  `json:"updated_at"`
	StartedAt       string  `json:"started_at,omitempty"`
	URL             string  `json:"url"`
	RunNumber       int     `json:"run_number"`
	WorkflowID      int64   `json:"workflow_id"`
	DurationSeconds float64 `json:"duration,omitempty"`
}

type Workflow struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Path  string `json:"path"`
	State string `json:"state"`
}

// WorkflowRunMinimal is a compact workflow run representation for reduced token usage
type WorkflowRunMinimal struct {
	ID              int64   `json:"id"`
	Name            string  `json:"name"`
	Status          string  `json:"status"`
	Conclusion      string  `json:"conclusion,omitempty"`
	CreatedAt       string  `json:"created_at"`
	DurationSeconds float64 `json:"duration,omitempty"`
}

// WorkflowRunCompact extends Minimal with additional fields
type WorkflowRunCompact struct {
	WorkflowRunMinimal
	Branch string `json:"branch,omitempty"`
	SHA    string `json:"sha,omitempty"`
	Event  string `json:"event,omitempty"`
	Actor  string `json:"actor,omitempty"`
	URL    string `json:"url,omitempty"`
}

// WorkflowRunFull is the complete workflow run representation
type WorkflowRunFull struct {
	ID              int64   `json:"id"`
	Name            string  `json:"name"`
	Status          string  `json:"status"`
	Conclusion      string  `json:"conclusion"`
	Branch          string  `json:"branch"`
	Event           string  `json:"event"`
	Actor           string  `json:"actor"`
	CreatedAt       string  `json:"created_at"`
	UpdatedAt       string  `json:"updated_at"`
	URL             string  `json:"url"`
	RunNumber       int     `json:"run_number"`
	WorkflowID      int64   `json:"workflow_id"`
	HeadSHA         string  `json:"head_sha"`
	StartedAt       string  `json:"started_at,omitempty"`
	CompletedAt     string  `json:"completed_at,omitempty"`
	DurationSeconds float64 `json:"duration,omitempty"`
}

// Step represents a single step within a workflow job
type Step struct {
	Name            string  `json:"name"`
	Number          int64   `json:"number"`
	Status          string  `json:"status"`
	Conclusion      string  `json:"conclusion,omitempty"`
	StartedAt       string  `json:"started_at,omitempty"`
	CompletedAt     string  `json:"completed_at,omitempty"`
	DurationSeconds float64 `json:"duration,omitempty"`
}

// Job represents a workflow run job
type Job struct {
	ID              int64    `json:"id"`
	Name            string   `json:"name"`
	Status          string   `json:"status"`
	Conclusion      string   `json:"conclusion,omitempty"`
	StartedAt       string   `json:"started_at,omitempty"`
	CompletedAt     string   `json:"completed_at,omitempty"`
	DurationSeconds float64  `json:"duration_seconds,omitempty"`
	RunnerName      string   `json:"runner_name,omitempty"`
	RunnerGroup     string   `json:"runner_group,omitempty"`
	Labels          []string `json:"labels,omitempty"`
	WorkflowRunID   int64    `json:"workflow_run_id"`
	Steps           []*Step  `json:"steps,omitempty"`
}

// TimingAnalysisOptions contains parameters for workflow/job/step timing analysis.
type TimingAnalysisOptions struct {
	Workflow   string
	RunID      int64
	Branch     string
	JobName    string
	StepName   string
	Limit      int
	Conclusion string
}

// TimingStats summarizes durations across a set of runs.
type TimingStats struct {
	Count          int     `json:"count"`
	AverageSeconds float64 `json:"average_seconds"`
	MedianSeconds  float64 `json:"median_seconds"`
	MinSeconds     float64 `json:"min_seconds"`
	MaxSeconds     float64 `json:"max_seconds"`
}

// TimingSample captures a single workflow/job/step duration in a given run.
type TimingSample struct {
	RunID           int64   `json:"run_id"`
	RunNumber       int     `json:"run_number"`
	CreatedAt       string  `json:"created_at,omitempty"`
	Conclusion      string  `json:"conclusion,omitempty"`
	DurationSeconds float64 `json:"duration_seconds"`
}

// TimingComparison compares a focus run against recent history.
type TimingComparison struct {
	*TimingSample
	DeltaFromAverageSeconds  float64 `json:"delta_from_average_seconds"`
	DeltaFromAveragePercent  float64 `json:"delta_from_average_percent"`
	DeltaFromPreviousSeconds float64 `json:"delta_from_previous_seconds,omitempty"`
	DeltaFromPreviousPercent float64 `json:"delta_from_previous_percent,omitempty"`
}

// TimingBreakdownItem highlights a job or step within the focus run.
type TimingBreakdownItem struct {
	JobName                 string  `json:"job_name,omitempty"`
	StepName                string  `json:"step_name,omitempty"`
	DurationSeconds         float64 `json:"duration_seconds"`
	AverageDurationSeconds  float64 `json:"average_duration_seconds"`
	DeltaFromAverageSeconds float64 `json:"delta_from_average_seconds"`
	DeltaFromAveragePercent float64 `json:"delta_from_average_percent"`
}

// TimingAnalysis is the full result of comparing workflow/job/step timings.
type TimingAnalysis struct {
	Scope         string                 `json:"scope"`
	WorkflowID    int64                  `json:"workflow_id"`
	WorkflowName  string                 `json:"workflow_name"`
	Branch        string                 `json:"branch,omitempty"`
	JobName       string                 `json:"job_name,omitempty"`
	StepName      string                 `json:"step_name,omitempty"`
	SampleCount   int                    `json:"sample_count"`
	Statistics    *TimingStats           `json:"statistics"`
	Focus         *TimingComparison      `json:"focus"`
	RecentSamples []*TimingSample        `json:"recent_samples"`
	JobBreakdown  []*TimingBreakdownItem `json:"job_breakdown,omitempty"`
	StepBreakdown []*TimingBreakdownItem `json:"step_breakdown,omitempty"`
}

// Artifact represents a workflow run artifact
type Artifact struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	SizeInBytes int64  `json:"size_in_bytes"`
	CreatedAt   string `json:"created_at"`
	ExpiresAt   string `json:"expires_at,omitempty"`
	ArchiveURL  string `json:"archive_url,omitempty"`
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
	Name      string `json:"name"`
	ID        int64  `json:"id"`
	SavedPath string `json:"saved_path"`
	FileCount int    `json:"file_count"`
	TotalSize int64  `json:"total_size"`
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
	SHA          string         `json:"sha"`
	State        string         `json:"state"` // "pending", "success", "failure", "neutral"
	TotalCount   int            `json:"total_count"`
	CheckRuns    []*CheckRun    `json:"check_runs"`
	ByConclusion map[string]int `json:"by_conclusion"`
}

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
}

// GetCheckRunsOptions contains parameters for getting check runs
type GetCheckRunsOptions struct {
	CheckName string // Optional: filter by check name
	Status    string // Optional: queued, in_progress, completed
	Filter    string // Optional: "latest" (default) or "all"
}

// workflowRunFromGitHub converts a github.WorkflowRun to our WorkflowRun type
func workflowRunFromGitHub(run *github.WorkflowRun) *WorkflowRun {
	updatedAt := run.GetUpdatedAt()
	return &WorkflowRun{
		ID:              run.GetID(),
		Name:            run.GetName(),
		Status:          run.GetStatus(),
		Conclusion:      run.GetConclusion(),
		Branch:          run.GetHeadBranch(),
		HeadSHA:         run.GetHeadSHA(),
		Event:           run.GetEvent(),
		Actor:           run.GetActor().GetLogin(),
		CreatedAt:       run.GetCreatedAt().String(),
		UpdatedAt:       updatedAt.String(),
		StartedAt:       formatTime(run.RunStartedAt),
		URL:             run.GetHTMLURL(),
		RunNumber:       run.GetRunNumber(),
		WorkflowID:      run.GetWorkflowID(),
		DurationSeconds: durationSeconds(run.RunStartedAt, &updatedAt),
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
	cleanup := func() {}
	defer func() { cleanup() }()

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

	// Sort by filename for consistent output
	sort.Slice(logFiles, func(i, j int) bool {
		return logFiles[i].name < logFiles[j].name
	})

	return logFiles, contentLength, nil
}

// GetWorkflowLogFiles returns a list of log files available in the workflow run archive
func (c *Client) GetWorkflowLogFiles(ctx context.Context, runID int64) ([]*LogFileInfo, error) {
	// Get the log archive URL
	url, resp, err := c.gh.Actions.GetWorkflowRunLogs(ctx, c.owner, c.repo, runID, maxRedirects)
	if err != nil {
		return nil, fmt.Errorf("failed to get workflow log URL for run %d: %w", runID, err)
	}

	if resp != nil && resp.StatusCode != 0 {
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusFound {
			return nil, newHTTPErrorFromGitHub(resp, "failed to get workflow logs")
		}
	}

	// Fetch ZIP archive (use unauthenticated client for pre-signed storage URLs)
	logFiles, _, err := readZipArchive(url.String(), presignedHTTPClient)
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
	url, resp, err := c.gh.Actions.GetWorkflowRunLogs(ctx, c.owner, c.repo, runID, maxRedirects)
	if err != nil {
		return "", fmt.Errorf("failed to get workflow log URL for run %d: %w", runID, err)
	}

	if resp != nil && resp.StatusCode != 0 {
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusFound {
			return "", newHTTPErrorFromGitHub(resp, "failed to get workflow logs")
		}
	}

	// Read ZIP archive (use unauthenticated client for pre-signed storage URLs)
	logFiles, _, err := readZipArchive(url.String(), presignedHTTPClient)
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

// AnalyzeTiming compares workflow, job, or step durations across recent runs.
func (c *Client) AnalyzeTiming(ctx context.Context, opts *TimingAnalysisOptions) (*TimingAnalysis, error) {
	if opts == nil {
		return nil, fmt.Errorf("timing analysis options are required")
	}
	if opts.StepName != "" && opts.JobName == "" {
		return nil, fmt.Errorf("job_name is required when step_name is provided")
	}

	limit := opts.Limit
	if limit <= 0 {
		limit = 10
	}

	scope := "workflow"
	if opts.StepName != "" {
		scope = "step"
	} else if opts.JobName != "" {
		scope = "job"
	}

	var (
		focusRun     *WorkflowRun
		workflowID   int64
		workflowName string
		err          error
	)

	workflowSelector := strings.TrimSpace(opts.Workflow)
	if opts.RunID > 0 {
		focusRun, err = c.GetWorkflowRun(ctx, opts.RunID)
		if err != nil {
			return nil, fmt.Errorf("failed to get workflow run %d: %w", opts.RunID, err)
		}
		if focusRun.Status != "completed" {
			return nil, fmt.Errorf("workflow run %d is %s; timing analysis requires a completed run", opts.RunID, focusRun.Status)
		}
		if workflowSelector != "" {
			workflowID, workflowName, err = c.ResolveWorkflowID(ctx, workflowSelector)
			if err != nil {
				return nil, fmt.Errorf("failed to resolve workflow %q: %w", workflowSelector, err)
			}
			if workflowID != focusRun.WorkflowID {
				return nil, fmt.Errorf("run %d belongs to workflow %q (%d), not %q (%d)", opts.RunID, focusRun.Name, focusRun.WorkflowID, workflowName, workflowID)
			}
		} else {
			workflowID = focusRun.WorkflowID
			workflowName = focusRun.Name
		}
		if opts.Branch == "" {
			opts.Branch = focusRun.Branch
		}
		if opts.Conclusion != "" && focusRun.Conclusion != opts.Conclusion {
			return nil, fmt.Errorf("run %d concluded as %s, which does not match conclusion filter %q", opts.RunID, focusRun.Conclusion, opts.Conclusion)
		}
	} else {
		if workflowSelector == "" {
			return nil, fmt.Errorf("workflow is required when run_id is not provided")
		}
		workflowID, workflowName, err = c.ResolveWorkflowID(ctx, workflowSelector)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve workflow %q: %w", workflowSelector, err)
		}
	}

	runs, err := c.listWorkflowRunsForTiming(ctx, workflowID, opts.Branch, limit)
	if err != nil {
		return nil, err
	}

	filteredRuns := make([]*WorkflowRun, 0, len(runs)+1)
	for _, run := range runs {
		if !matchesTimingRun(run, opts.Conclusion) {
			continue
		}
		filteredRuns = append(filteredRuns, run)
	}
	if focusRun != nil && matchesTimingRun(focusRun, opts.Conclusion) {
		filteredRuns = appendTimingRunIfMissing(filteredRuns, focusRun)
	}

	sort.Slice(filteredRuns, func(i, j int) bool {
		return filteredRuns[i].RunNumber > filteredRuns[j].RunNumber
	})
	filteredRuns = limitTimingRuns(filteredRuns, limit, opts.RunID)

	if len(filteredRuns) == 0 {
		return nil, fmt.Errorf("no completed runs with timing data found for workflow %q", workflowName)
	}

	if focusRun == nil {
		focusRun = filteredRuns[0]
	}

	jobsByRun := make(map[int64][]*Job, len(filteredRuns))
	for _, run := range filteredRuns {
		jobs, err := c.GetWorkflowJobs(ctx, run.ID, "", 0)
		if err != nil {
			return nil, fmt.Errorf("failed to get jobs for run %d: %w", run.ID, err)
		}
		jobsByRun[run.ID] = jobs
	}

	samples := make([]*TimingSample, 0, len(filteredRuns))
	for _, run := range filteredRuns {
		sample, err := timingSampleForScope(run, jobsByRun[run.ID], scope, opts.JobName, opts.StepName)
		if err != nil {
			return nil, err
		}
		if sample != nil {
			samples = append(samples, sample)
		}
	}

	if len(samples) == 0 {
		return nil, fmt.Errorf("no timing samples matched scope %q for workflow %q", scope, workflowName)
	}

	focusIndex := indexOfTimingSample(samples, focusRun.ID)
	if focusIndex == -1 {
		return nil, fmt.Errorf("run %d does not include the requested %s", focusRun.ID, scope)
	}

	baselineSamples := samplesExcludingIndex(samples, focusIndex)
	if len(baselineSamples) == 0 {
		baselineSamples = samples
	}

	analysis := &TimingAnalysis{
		Scope:         scope,
		WorkflowID:    workflowID,
		WorkflowName:  workflowName,
		Branch:        opts.Branch,
		JobName:       opts.JobName,
		StepName:      opts.StepName,
		SampleCount:   len(samples),
		Statistics:    timingStatsFromSamples(samples),
		Focus:         compareTimingSample(samples[focusIndex], timingStatsFromSamples(baselineSamples), previousTimingSample(samples, focusIndex)),
		RecentSamples: samples,
	}

	switch scope {
	case "workflow":
		analysis.JobBreakdown = buildJobBreakdown(jobsByRun, focusRun.ID)
		analysis.StepBreakdown = buildStepBreakdown(jobsByRun, focusRun.ID, opts.JobName)
	case "job":
		analysis.StepBreakdown = buildStepBreakdown(jobsByRun, focusRun.ID, opts.JobName)
	}

	return analysis, nil
}

func (c *Client) listWorkflowRunsForTiming(ctx context.Context, workflowID int64, branch string, limit int) ([]*WorkflowRun, error) {
	perPage := c.perPageLimit
	target := limit * 3
	if target < 20 {
		target = 20
	}
	if perPage < target {
		perPage = target
	}
	if perPage > 100 {
		perPage = 100
	}
	if perPage <= 0 {
		perPage = 50
	}

	opts := &github.ListWorkflowRunsOptions{
		ListOptions: github.ListOptions{PerPage: perPage},
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

func matchesTimingRun(run *WorkflowRun, conclusion string) bool {
	if run == nil || run.Status != "completed" || run.DurationSeconds <= 0 {
		return false
	}
	if conclusion != "" && run.Conclusion != conclusion {
		return false
	}
	return true
}

func appendTimingRunIfMissing(runs []*WorkflowRun, focus *WorkflowRun) []*WorkflowRun {
	for _, run := range runs {
		if run.ID == focus.ID {
			return runs
		}
	}
	return append(runs, focus)
}

func limitTimingRuns(runs []*WorkflowRun, limit int, focusRunID int64) []*WorkflowRun {
	if limit <= 0 || len(runs) <= limit {
		return runs
	}
	if focusRunID == 0 {
		return runs[:limit]
	}

	focusIndex := -1
	for i, run := range runs {
		if run.ID == focusRunID {
			focusIndex = i
			break
		}
	}
	if focusIndex == -1 || focusIndex < limit {
		return runs[:limit]
	}

	limited := append([]*WorkflowRun{}, runs[:limit-1]...)
	limited = append(limited, runs[focusIndex])
	sort.Slice(limited, func(i, j int) bool {
		return limited[i].RunNumber > limited[j].RunNumber
	})
	return limited
}

func timingSampleForScope(run *WorkflowRun, jobs []*Job, scope, jobName, stepName string) (*TimingSample, error) {
	switch scope {
	case "workflow":
		return newTimingSample(run, run.DurationSeconds), nil
	case "job":
		job := findJobByName(jobs, jobName)
		if job == nil {
			return nil, nil
		}
		if job.DurationSeconds <= 0 {
			return nil, nil
		}
		return newTimingSample(run, job.DurationSeconds), nil
	case "step":
		job := findJobByName(jobs, jobName)
		if job == nil {
			return nil, nil
		}
		step := findStepByName(job.Steps, stepName)
		if step == nil || step.DurationSeconds <= 0 {
			return nil, nil
		}
		return newTimingSample(run, step.DurationSeconds), nil
	default:
		return nil, fmt.Errorf("unknown timing scope %q", scope)
	}
}

func newTimingSample(run *WorkflowRun, duration float64) *TimingSample {
	return &TimingSample{
		RunID:           run.ID,
		RunNumber:       run.RunNumber,
		CreatedAt:       run.CreatedAt,
		Conclusion:      run.Conclusion,
		DurationSeconds: duration,
	}
}

func findJobByName(jobs []*Job, name string) *Job {
	normalized := normalizeTimingName(name)
	for _, job := range jobs {
		if normalizeTimingName(job.Name) == normalized {
			return job
		}
	}
	return nil
}

func findStepByName(steps []*Step, name string) *Step {
	normalized := normalizeTimingName(name)
	for _, step := range steps {
		if normalizeTimingName(step.Name) == normalized {
			return step
		}
	}
	return nil
}

func normalizeTimingName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func indexOfTimingSample(samples []*TimingSample, runID int64) int {
	for i, sample := range samples {
		if sample.RunID == runID {
			return i
		}
	}
	return -1
}

func samplesExcludingIndex(samples []*TimingSample, exclude int) []*TimingSample {
	if exclude < 0 || exclude >= len(samples) {
		return samples
	}
	result := make([]*TimingSample, 0, len(samples)-1)
	for i, sample := range samples {
		if i == exclude {
			continue
		}
		result = append(result, sample)
	}
	return result
}

func previousTimingSample(samples []*TimingSample, focusIndex int) *TimingSample {
	if focusIndex < 0 || focusIndex+1 >= len(samples) {
		return nil
	}
	return samples[focusIndex+1]
}

func timingStatsFromSamples(samples []*TimingSample) *TimingStats {
	if len(samples) == 0 {
		return &TimingStats{}
	}

	durations := make([]float64, 0, len(samples))
	for _, sample := range samples {
		durations = append(durations, sample.DurationSeconds)
	}
	sort.Float64s(durations)

	var total float64
	for _, duration := range durations {
		total += duration
	}

	median := durations[len(durations)/2]
	if len(durations)%2 == 0 {
		middle := len(durations) / 2
		median = (durations[middle-1] + durations[middle]) / 2
	}

	return &TimingStats{
		Count:          len(durations),
		AverageSeconds: total / float64(len(durations)),
		MedianSeconds:  median,
		MinSeconds:     durations[0],
		MaxSeconds:     durations[len(durations)-1],
	}
}

func compareTimingSample(sample *TimingSample, baseline *TimingStats, previous *TimingSample) *TimingComparison {
	comparison := &TimingComparison{
		TimingSample:            sample,
		DeltaFromAverageSeconds: sample.DurationSeconds - baseline.AverageSeconds,
		DeltaFromAveragePercent: deltaPercent(sample.DurationSeconds-baseline.AverageSeconds, baseline.AverageSeconds),
	}
	if previous != nil {
		comparison.DeltaFromPreviousSeconds = sample.DurationSeconds - previous.DurationSeconds
		comparison.DeltaFromPreviousPercent = deltaPercent(sample.DurationSeconds-previous.DurationSeconds, previous.DurationSeconds)
	}
	return comparison
}

func deltaPercent(delta, baseline float64) float64 {
	if baseline == 0 {
		return 0
	}
	return (delta / baseline) * 100
}

type stepBreakdownKey struct {
	JobName  string
	StepName string
}

func buildJobBreakdown(jobsByRun map[int64][]*Job, focusRunID int64) []*TimingBreakdownItem {
	focusJobs := jobsByRun[focusRunID]
	baseline := make(map[string][]float64)
	for runID, jobs := range jobsByRun {
		if runID == focusRunID {
			continue
		}
		for _, job := range jobs {
			if job.DurationSeconds <= 0 {
				continue
			}
			key := normalizeTimingName(job.Name)
			baseline[key] = append(baseline[key], job.DurationSeconds)
		}
	}

	items := make([]*TimingBreakdownItem, 0, len(focusJobs))
	for _, job := range focusJobs {
		if job.DurationSeconds <= 0 {
			continue
		}
		stats := timingStatsFromDurations(baseline[normalizeTimingName(job.Name)])
		delta := job.DurationSeconds - stats.AverageSeconds
		items = append(items, &TimingBreakdownItem{
			JobName:                 job.Name,
			DurationSeconds:         job.DurationSeconds,
			AverageDurationSeconds:  stats.AverageSeconds,
			DeltaFromAverageSeconds: delta,
			DeltaFromAveragePercent: deltaPercent(delta, stats.AverageSeconds),
		})
	}
	sortTimingBreakdown(items)
	return limitTimingBreakdown(items, 10)
}

func buildStepBreakdown(jobsByRun map[int64][]*Job, focusRunID int64, jobName string) []*TimingBreakdownItem {
	focusJobs := jobsByRun[focusRunID]
	baseline := make(map[stepBreakdownKey][]float64)
	normalizedJobName := normalizeTimingName(jobName)
	for runID, jobs := range jobsByRun {
		if runID == focusRunID {
			continue
		}
		for _, job := range jobs {
			if normalizedJobName != "" && normalizeTimingName(job.Name) != normalizedJobName {
				continue
			}
			for _, step := range job.Steps {
				if step.DurationSeconds <= 0 {
					continue
				}
				key := stepBreakdownKey{
					JobName:  normalizeTimingName(job.Name),
					StepName: normalizeTimingName(step.Name),
				}
				baseline[key] = append(baseline[key], step.DurationSeconds)
			}
		}
	}

	items := make([]*TimingBreakdownItem, 0)
	for _, job := range focusJobs {
		if normalizedJobName != "" && normalizeTimingName(job.Name) != normalizedJobName {
			continue
		}
		for _, step := range job.Steps {
			if step.DurationSeconds <= 0 {
				continue
			}
			key := stepBreakdownKey{
				JobName:  normalizeTimingName(job.Name),
				StepName: normalizeTimingName(step.Name),
			}
			stats := timingStatsFromDurations(baseline[key])
			delta := step.DurationSeconds - stats.AverageSeconds
			items = append(items, &TimingBreakdownItem{
				JobName:                 job.Name,
				StepName:                step.Name,
				DurationSeconds:         step.DurationSeconds,
				AverageDurationSeconds:  stats.AverageSeconds,
				DeltaFromAverageSeconds: delta,
				DeltaFromAveragePercent: deltaPercent(delta, stats.AverageSeconds),
			})
		}
	}
	sortTimingBreakdown(items)
	return limitTimingBreakdown(items, 10)
}

func timingStatsFromDurations(durations []float64) *TimingStats {
	samples := make([]*TimingSample, 0, len(durations))
	for _, duration := range durations {
		samples = append(samples, &TimingSample{DurationSeconds: duration})
	}
	return timingStatsFromSamples(samples)
}

func sortTimingBreakdown(items []*TimingBreakdownItem) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].DeltaFromAverageSeconds == items[j].DeltaFromAverageSeconds {
			return items[i].DurationSeconds > items[j].DurationSeconds
		}
		return items[i].DeltaFromAverageSeconds > items[j].DeltaFromAverageSeconds
	})
}

func limitTimingBreakdown(items []*TimingBreakdownItem, limit int) []*TimingBreakdownItem {
	if len(items) <= limit {
		return items
	}
	return items[:limit]
}

// GetWorkflowJobLogs retrieves logs for a specific job
func (c *Client) GetWorkflowJobLogs(ctx context.Context, jobID int64, head, tail, offset int, noHeaders bool, filterOpts *LogFilterOptions) (string, error) {
	// Get the log archive
	url, resp, err := c.gh.Actions.GetWorkflowJobLogs(ctx, c.owner, c.repo, jobID, maxRedirects)
	if err != nil {
		return "", fmt.Errorf("failed to get job log URL for job %d: %w", jobID, err)
	}

	// Check response status
	if resp != nil && resp.StatusCode != 0 {
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusFound {
			return "", newHTTPErrorFromGitHub(resp, "failed to get job logs")
		}
	}

	// Fetch the redirected payload URL without auth headers.
	// Some storage backends reject Authorization headers on pre-signed URLs.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url.String(), nil)
	if err != nil {
		return "", fmt.Errorf("failed to build job log request for job %d: %w", jobID, err)
	}
	zipResp, err := presignedHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to fetch job logs for job %d: %w", jobID, err)
	}
	defer zipResp.Body.Close()

	if zipResp.StatusCode != http.StatusOK {
		return "", &HTTPError{StatusCode: zipResp.StatusCode, Message: fmt.Sprintf("failed to fetch job logs: HTTP %d", zipResp.StatusCode)}
	}

	// Read the payload data (may be ZIP or plain text), bounded to maxLogFileSize.
	zipData, err := io.ReadAll(io.LimitReader(zipResp.Body, maxLogFileSize))
	if err != nil {
		return "", fmt.Errorf("failed to read job logs for job %d: %w", jobID, err)
	}

	// Collect all log files from ZIP payload when available.
	// GitHub may also return plain text for job log downloads.
	var logFiles []logFile
	if zipReader, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData))); err == nil {
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
	} else {
		logFiles = append(logFiles, logFile{
			name: fmt.Sprintf("job-%d.log", jobID),
			data: string(zipData),
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
	zipURL, resp, err := c.gh.Actions.DownloadArtifact(ctx, c.owner, c.repo, artifactID, maxRedirects)
	if err != nil {
		return nil, fmt.Errorf("failed to get artifact download URL: %w", err)
	}

	if resp != nil && resp.StatusCode != 0 {
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusFound {
			return nil, fmt.Errorf("failed to download artifact: HTTP %d", resp.StatusCode)
		}
	}

	// Fetch the ZIP from the pre-signed URL without auth headers.
	// Storage backends reject Authorization headers on pre-signed URLs.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, zipURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build artifact request: %w", err)
	}
	zipResp, err := presignedHTTPClient.Do(req)
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
	zipURL, resp, err := c.gh.Actions.DownloadArtifact(ctx, c.owner, c.repo, artifactID, maxRedirects)
	if err != nil {
		return nil, fmt.Errorf("failed to get artifact download URL: %w", err)
	}

	if resp != nil && resp.StatusCode != 0 {
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusFound {
			return nil, fmt.Errorf("failed to download artifact: HTTP %d", resp.StatusCode)
		}
	}

	// Fetch the ZIP from the pre-signed URL without auth headers.
	// Storage backends reject Authorization headers on pre-signed URLs.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, zipURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build artifact request: %w", err)
	}
	zipResp, err := presignedHTTPClient.Do(req)
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

	// Track whether write succeeded; remove partial file on failure
	writeSucceeded := false
	defer func() {
		if !writeSucceeded {
			os.Remove(outputPath)
		}
	}()

	// Copy data to file
	bytesWritten, err := io.Copy(outFile, zipResp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to write artifact to file: %w", err)
	}
	writeSucceeded = true

	// Count files in the archive
	if _, err := outFile.Seek(0, 0); err != nil {
		return nil, fmt.Errorf("failed to seek artifact file: %w", err)
	}
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

// GetCheckRunsForRef retrieves status information for a ref using workflow runs.
// It intentionally avoids the GitHub Checks API because many PATs cannot access it.
func (c *Client) GetCheckRunsForRef(ctx context.Context, ref string, opts *GetCheckRunsOptions) (*CombinedCheckStatus, error) {
	if ref == "" {
		commit, err := GetLastCommit()
		if err != nil {
			return nil, fmt.Errorf("failed to get current commit: %w", err)
		}
		ref = commit.SHA
	}

	runOpts := &github.ListWorkflowRunsOptions{
		ListOptions: github.ListOptions{PerPage: c.perPageLimit},
	}

	refIsCommit := isLikelyCommitRef(ref)
	if !refIsCommit {
		runOpts.Branch = ref
	}

	runs, _, err := c.gh.Actions.ListRepositoryWorkflowRuns(ctx, c.owner, c.repo, runOpts)
	if err != nil {
		return nil, fmt.Errorf("failed to list workflow runs for ref %s: %w", ref, err)
	}

	filterByName := ""
	filterByStatus := ""
	filterMode := "latest"
	if opts != nil {
		filterByName = opts.CheckName
		filterByStatus = opts.Status
		if opts.Filter == "all" {
			filterMode = "all"
		}
	}

	filtered := make([]*github.WorkflowRun, 0)
	for _, run := range runs.WorkflowRuns {
		if run == nil {
			continue
		}
		if refIsCommit {
			headSHA := strings.ToLower(run.GetHeadSHA())
			if !strings.HasPrefix(headSHA, strings.ToLower(ref)) {
				continue
			}
		}
		if filterByName != "" && run.GetName() != filterByName {
			continue
		}
		if filterByStatus != "" && run.GetStatus() != filterByStatus {
			continue
		}
		filtered = append(filtered, run)
	}

	if filterMode != "all" {
		latestByName := make(map[string]*github.WorkflowRun)
		for _, run := range filtered {
			name := run.GetName()
			if existing, ok := latestByName[name]; !ok {
				latestByName[name] = run
			} else {
				if run.GetRunNumber() > existing.GetRunNumber() {
					latestByName[name] = run
				}
			}
		}
		deduped := make([]*github.WorkflowRun, 0, len(latestByName))
		for _, run := range latestByName {
			deduped = append(deduped, run)
		}
		filtered = deduped
	}

	result := &CombinedCheckStatus{
		SHA:          ref,
		CheckRuns:    make([]*CheckRun, 0),
		ByConclusion: make(map[string]int),
	}

	// Convert workflow runs to check-like entries.
	for _, run := range filtered {
		checkRun := &CheckRun{
			ID:          run.GetID(),
			Name:        run.GetName(),
			Status:      run.GetStatus(),
			Conclusion:  run.GetConclusion(),
			StartedAt:   formatTime(run.RunStartedAt),
			CompletedAt: formatTime(run.UpdatedAt),
			AppName:     "github-actions",
			DetailsURL:  run.GetHTMLURL(),
		}
		result.CheckRuns = append(result.CheckRuns, checkRun)

		// Count by conclusion
		if run.GetConclusion() != "" {
			result.ByConclusion[run.GetConclusion()]++
		} else if run.GetStatus() != "completed" {
			result.ByConclusion[run.GetStatus()]++
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

// formatTime formats a github.Timestamp pointer into an ISO string
func formatTime(t *github.Timestamp) string {
	if t == nil {
		return ""
	}
	return t.String()
}

// durationSeconds returns the elapsed seconds between two timestamps.
// Returns 0 if either timestamp is nil or the duration is negative.
func durationSeconds(start, end *github.Timestamp) float64 {
	if start == nil || end == nil {
		return 0
	}
	d := end.Time.Sub(start.Time).Seconds()
	if d < 0 {
		return 0
	}
	return d
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
	Name    string `json:"name"`
	Line    int    `json:"line"`
	JobName string `json:"job_name,omitempty"`
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

// FailureDiagnosis is the top-level result of diagnosing a failed workflow run
type FailureDiagnosis struct {
	RunID      int64          `json:"run_id"`
	RunName    string         `json:"run_name"`
	RunURL     string         `json:"run_url"`
	Branch     string         `json:"branch"`
	HeadSHA    string         `json:"head_sha"`
	Conclusion string         `json:"conclusion"`
	FailedJobs []*FailedJob   `json:"failed_jobs"`
	Flakiness  *FlakinessInfo `json:"flakiness,omitempty"`
	Summary    string         `json:"summary"`
}

// FailedJob represents a job that failed within a workflow run
type FailedJob struct {
	JobID       int64         `json:"job_id"`
	JobName     string        `json:"job_name"`
	Conclusion  string        `json:"conclusion"`
	FailedSteps []*FailedStep `json:"failed_steps"`
	ErrorLines  []string      `json:"error_lines"`
}

// FailedStep represents a step that failed within a job
type FailedStep struct {
	Name       string `json:"name"`
	Number     int64  `json:"number"`
	Conclusion string `json:"conclusion"`
}

// FlakinessInfo contains information about whether this failure is likely a flake
type FlakinessInfo struct {
	RecentRuns       int    `json:"recent_runs_checked"`
	RecentFailures   int    `json:"recent_failures"`
	RecentSuccesses  int    `json:"recent_successes"`
	SameFailureCount int    `json:"same_failure_count"`
	Verdict          string `json:"verdict"` // "likely_flake", "likely_regression", "first_failure", "unknown"
}

// errorPatterns are regex patterns that identify error lines in CI logs
var errorPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)^.*error[:\[].*`),
	regexp.MustCompile(`(?i)^.*FAIL[:\s].*`),
	regexp.MustCompile(`(?i)^.*fatal[:\s].*`),
	regexp.MustCompile(`(?i)^.*panic[:\s].*`),
	regexp.MustCompile(`(?i)^.*exception[:\s].*`),
	regexp.MustCompile(`(?i)^.*traceback.*`),
	regexp.MustCompile(`(?i)^E\s+\w+`),        // pytest-style "E   AssertionError"
	regexp.MustCompile(`--- FAIL:`),           // Go test failures
	regexp.MustCompile(`(?i)exit code [1-9]`), // non-zero exit codes
	regexp.MustCompile(`(?i)command.*failed`),
	regexp.MustCompile(`(?i)process completed with exit code [1-9]`),
	regexp.MustCompile(`##\[error\]`), // GitHub Actions error annotations
}

// DiagnoseFailure performs a comprehensive diagnosis of a failed workflow run.
// It fetches the run, identifies failed jobs and steps, extracts error lines from
// logs, and optionally checks for flakiness by comparing against recent runs.
func (c *Client) DiagnoseFailure(ctx context.Context, runID int64, checkFlakiness bool, maxLogLines int) (*FailureDiagnosis, error) {
	if maxLogLines <= 0 {
		maxLogLines = 200
	}

	// 1. Get the run info
	run, err := c.GetWorkflowRun(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("failed to get run %d: %w", runID, err)
	}

	diagnosis := &FailureDiagnosis{
		RunID:      run.ID,
		RunName:    run.Name,
		RunURL:     run.URL,
		Branch:     run.Branch,
		HeadSHA:    run.HeadSHA,
		Conclusion: run.Conclusion,
	}

	if run.Status != "completed" {
		diagnosis.Summary = fmt.Sprintf("Run %d is still %s (not completed yet)", runID, run.Status)
		return diagnosis, nil
	}

	if run.Conclusion == "success" {
		diagnosis.Summary = fmt.Sprintf("Run %d succeeded — nothing to diagnose", runID)
		return diagnosis, nil
	}

	// 2. Get jobs and identify failures
	jobs, err := c.GetWorkflowJobs(ctx, runID, "", 0)
	if err != nil {
		return nil, fmt.Errorf("failed to get jobs for run %d: %w", runID, err)
	}

	for _, job := range jobs {
		if job.Conclusion != "failure" && job.Conclusion != "cancelled" && job.Conclusion != "timed_out" {
			continue
		}

		failedJob := &FailedJob{
			JobID:      job.ID,
			JobName:    job.Name,
			Conclusion: job.Conclusion,
		}

		// Identify failed steps
		for _, step := range job.Steps {
			if step.Conclusion == "failure" || step.Conclusion == "cancelled" || step.Conclusion == "timed_out" {
				failedJob.FailedSteps = append(failedJob.FailedSteps, &FailedStep{
					Name:       step.Name,
					Number:     step.Number,
					Conclusion: step.Conclusion,
				})
			}
		}

		// 3. Extract error lines from job logs
		errorLines := c.extractErrorLines(ctx, job.ID, maxLogLines)
		failedJob.ErrorLines = errorLines

		diagnosis.FailedJobs = append(diagnosis.FailedJobs, failedJob)
	}

	// 4. Optional flakiness check
	if checkFlakiness && run.WorkflowID > 0 {
		diagnosis.Flakiness = c.checkFlakiness(ctx, run, diagnosis.FailedJobs)
	}

	// 5. Build summary
	diagnosis.Summary = c.buildDiagnosisSummary(diagnosis)

	return diagnosis, nil
}

// extractErrorLines fetches logs for a job and extracts lines matching error patterns
func (c *Client) extractErrorLines(ctx context.Context, jobID int64, maxLines int) []string {
	logs, err := c.GetWorkflowJobLogs(ctx, jobID, 0, 0, 0, true, nil)
	if err != nil {
		log.Debugf("Could not fetch logs for job %d: %v", jobID, err)
		return []string{fmt.Sprintf("[could not fetch logs: %v]", err)}
	}

	lines := strings.Split(logs, "\n")
	var errorLines []string
	seen := make(map[string]bool)

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		for _, pattern := range errorPatterns {
			if pattern.MatchString(trimmed) {
				// Remove timestamps that GitHub Actions prepends (e.g., "2024-01-15T10:30:00.1234567Z ")
				cleaned := trimmed
				if len(cleaned) > 30 && cleaned[4] == '-' && cleaned[10] == 'T' {
					if spaceIdx := strings.Index(cleaned, " "); spaceIdx > 0 && spaceIdx < 35 {
						cleaned = cleaned[spaceIdx+1:]
					}
				}
				if !seen[cleaned] {
					seen[cleaned] = true
					errorLines = append(errorLines, cleaned)
				}
				break
			}
		}

		if len(errorLines) >= maxLines {
			break
		}
	}

	return errorLines
}

// checkFlakiness compares the current failure against recent runs of the same workflow
func (c *Client) checkFlakiness(ctx context.Context, run *WorkflowRun, failedJobs []*FailedJob) *FlakinessInfo {
	info := &FlakinessInfo{}

	recentRuns, err := c.GetWorkflowRuns(ctx, run.WorkflowID, run.Branch)
	if err != nil {
		log.Debugf("Could not fetch recent runs for flakiness check: %v", err)
		info.Verdict = "unknown"
		return info
	}

	// Get the names of failed jobs for comparison
	failedJobNames := make(map[string]bool)
	for _, fj := range failedJobs {
		failedJobNames[fj.JobName] = true
	}

	maxCheck := 10
	sameFailures := 0
	successes := 0
	failures := 0
	checked := 0

	for _, r := range recentRuns {
		if r.ID == run.ID || r.Status != "completed" {
			continue
		}
		checked++
		if checked > maxCheck {
			break
		}

		switch r.Conclusion {
		case "success":
			successes++
		case "failure":
			failures++
			// Check if the same jobs failed
			jobs, err := c.GetWorkflowJobs(ctx, r.ID, "", 0)
			if err != nil {
				continue
			}
			for _, j := range jobs {
				if j.Conclusion == "failure" && failedJobNames[j.Name] {
					sameFailures++
					break
				}
			}
		}
	}

	info.RecentRuns = checked
	info.RecentFailures = failures
	info.RecentSuccesses = successes
	info.SameFailureCount = sameFailures

	switch {
	case checked == 0:
		info.Verdict = "unknown"
	case sameFailures >= 2 && successes > 0:
		info.Verdict = "likely_flake"
	case successes == 0 && failures > 0:
		info.Verdict = "likely_regression"
	case failures == 0:
		info.Verdict = "first_failure"
	default:
		info.Verdict = "likely_regression"
	}

	return info
}

// buildDiagnosisSummary creates a human-readable summary of the diagnosis
func (c *Client) buildDiagnosisSummary(d *FailureDiagnosis) string {
	var sb strings.Builder

	if len(d.FailedJobs) == 0 {
		sb.WriteString(fmt.Sprintf("Run %d concluded as %s but no failed jobs found (may be cancelled or skipped).", d.RunID, d.Conclusion))
		return sb.String()
	}

	jobNames := make([]string, 0, len(d.FailedJobs))
	totalErrors := 0
	for _, fj := range d.FailedJobs {
		jobNames = append(jobNames, fj.JobName)
		totalErrors += len(fj.ErrorLines)
	}

	sb.WriteString(fmt.Sprintf("%d failed job(s): %s. ", len(d.FailedJobs), strings.Join(jobNames, ", ")))
	sb.WriteString(fmt.Sprintf("%d error line(s) extracted from logs.", totalErrors))

	if d.Flakiness != nil {
		sb.WriteString(fmt.Sprintf(" Flakiness verdict: %s", d.Flakiness.Verdict))
		if d.Flakiness.Verdict == "likely_flake" {
			sb.WriteString(fmt.Sprintf(" (same job failed in %d of last %d runs, but %d succeeded).",
				d.Flakiness.SameFailureCount, d.Flakiness.RecentRuns, d.Flakiness.RecentSuccesses))
		} else {
			sb.WriteString(".")
		}
	}

	return sb.String()
}
