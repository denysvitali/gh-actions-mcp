package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/denysvitali/gh-actions-mcp/config"
	"github.com/denysvitali/gh-actions-mcp/github"
	ghapi "github.com/google/go-github/v89/github"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sirupsen/logrus"
)

type MCPServer struct {
	srv     *mcp.Server
	client  *github.Client
	config  *config.Config
	log     *logrus.Logger
	version string

	invokeMu            sync.Mutex
	invokeSession       *mcp.ClientSession
	invokeServerSession *mcp.ServerSession
	invokeCancel        context.CancelFunc
}

// Default limits for output control
const (
	DefaultListLimit = 5  // Default max items for lists (reduced from 10 for token efficiency)
	DefaultLogLines  = 50 // Default max lines for logs (reduced from 100 for token efficiency)
)

var validRunElements = []string{
	"info",
	"jobs",
	"logs",
	"log_files",
	"log_sections",
	"artifacts",
	"artifact_content",
}

func isValidRunElement(element string) bool {
	for _, e := range validRunElements {
		if element == e {
			return true
		}
	}
	return false
}

func formatWorkflowStatusSummary(ref string, status *github.CombinedCheckStatus, filterMode string) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Workflow Status for %s\n", ref))
	sb.WriteString(fmt.Sprintf("Overall: %s\n", status.State))
	sb.WriteString(fmt.Sprintf("Workflows: %d\n", status.TotalCount))
	sb.WriteString(fmt.Sprintf("Filter Mode: %s\n", filterMode))

	if len(status.ByConclusion) > 0 {
		keys := make([]string, 0, len(status.ByConclusion))
		for k := range status.ByConclusion {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		sb.WriteString("By Conclusion:\n")
		for _, k := range keys {
			sb.WriteString(fmt.Sprintf("  %s: %d\n", k, status.ByConclusion[k]))
		}
	}

	if len(status.CheckRuns) == 0 {
		sb.WriteString("No matching workflow statuses found.\n")
		sb.WriteString("Tip: try filter=\"all\" or provide a different ref (branch name or commit SHA).\n")
		return sb.String()
	}

	sb.WriteString("Workflow Details:\n")
	maxItems := len(status.CheckRuns)
	if maxItems > 20 {
		maxItems = 20
	}
	for i := 0; i < maxItems; i++ {
		r := status.CheckRuns[i]
		conclusion := r.Conclusion
		if conclusion == "" {
			conclusion = "-"
		}
		sb.WriteString(fmt.Sprintf("  - %s: %s/%s (id: %d)\n", r.Name, r.Status, conclusion, r.ID))
	}
	if len(status.CheckRuns) > maxItems {
		sb.WriteString(fmt.Sprintf("  ... and %d more\n", len(status.CheckRuns)-maxItems))
	}

	return sb.String()
}

// Helper functions to reduce repetition

// getLimit returns the limit from config or default
func (s *MCPServer) getLimit() int {
	if s.config.DefaultLimit > 0 {
		return s.config.DefaultLimit
	}
	return DefaultListLimit
}

// getLogLines returns the max log lines from config or default
func (s *MCPServer) getLogLines() int {
	if s.config.DefaultLogLen > 0 {
		return s.config.DefaultLogLen
	}
	return DefaultLogLines
}

func (s *MCPServer) formatAuthErrorWithRepo(err error, msg, repo string) string {
	errStr := ""
	if err != nil {
		errStr = strings.ToLower(err.Error())
	}

	if strings.Contains(errStr, "resource not accessible by personal access token") {
		return fmt.Sprintf("%s: %v\nGitHub rejected the token for this endpoint.\nFor fine-grained PATs, grant repository access plus:\n- Actions: Read (runs/jobs/logs/artifacts)\nFor classic PATs on private repos, include the 'repo' scope.", msg, err)
	}

	if strings.Contains(errStr, "401") || strings.Contains(errStr, "unauthorized") || strings.Contains(errStr, "bad credentials") || strings.Contains(errStr, "log access unauthorized") {
		return fmt.Sprintf("%s: %v\nGitHub rejected authentication for %s.\nSet a valid GITHUB_TOKEN and ensure it can read Actions data in this repository.", msg, err, repo)
	}

	var rateLimitErr *ghapi.RateLimitError
	if errors.As(err, &rateLimitErr) {
		return fmt.Sprintf("%s: GitHub API rate limit exceeded for %s.\nTry again later or use a token with higher rate limits.", msg, repo)
	}

	if strings.Contains(errStr, "403") || strings.Contains(errStr, "insufficient") || strings.Contains(errStr, "forbidden") {
		return fmt.Sprintf("%s: %v\nGitHub accepted authentication but denied authorization for %s.\nThe token likely lacks required repository permissions for this operation.", msg, err, repo)
	}

	if strings.Contains(errStr, "404") {
		return fmt.Sprintf("%s: %v\nGitHub returned 404 for %s.\nThis usually means the run/ref/artifact is not in this repository, or the token cannot see a private repository.", msg, err, repo)
	}

	if config.IsAuthenticationError(err) {
		return fmt.Sprintf("authentication failed: %v\nMake sure GITHUB_TOKEN is set (or run 'gh auth login' on macOS) and has access to %s", err, repo)
	}
	return fmt.Sprintf("%s: %v", msg, err)
}

// formatAuthError formats an error message with authentication context
func (s *MCPServer) formatAuthError(err error, msg string) string {
	repo := fmt.Sprintf("%s/%s", s.config.RepoOwner, s.config.RepoName)
	return s.formatAuthErrorWithRepo(err, msg, repo)
}

func (s *MCPServer) formatAuthErrorForRepo(err error, msg, owner, repo string) string {
	return s.formatAuthErrorWithRepo(err, msg, fmt.Sprintf("%s/%s", owner, repo))
}

// jsonResult returns a successful JSON response
func jsonResult(data interface{}) (*mcp.CallToolResult, error) {
	d, err := json.Marshal(data)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to marshal: %v", err)), nil
	}
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: string(d)}},
		StructuredContent: data,
	}, nil
}

// jsonResultPretty returns a successful JSON response with pretty formatting
func jsonResultPretty(data interface{}) (*mcp.CallToolResult, error) {
	d, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return errorResult(fmt.Sprintf("failed to marshal: %v", err)), nil
	}
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: string(d)}},
		StructuredContent: data,
	}, nil
}

// textResult returns a simple text response
func textResult(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: msg}}}
}

// truncateLogResult auto-truncates log output to the last defaultLines lines
// when the caller hasn't applied any explicit limiting parameters.
// Appends a banner with total line count and usage hints for the AI agent.
func truncateLogResult(logs string, defaultLines int, callerLimited bool) *mcp.CallToolResult {
	if callerLimited || logs == "" {
		return textResult(logs)
	}

	lines := strings.Split(logs, "\n")
	total := len(lines)

	// Trim trailing empty line from final newline
	if total > 0 && lines[total-1] == "" {
		total--
		lines = lines[:total]
	}

	if total <= defaultLines {
		return textResult(logs)
	}

	truncated := lines[total-defaultLines:]
	banner := fmt.Sprintf(
		"\n--- [showing last %d of %d lines] ---\nUse head/tail/offset/search/search_regex/section/file_pattern to refine.\nExample: tail=%d, or search=\"error\"",
		defaultLines, total, min(total, defaultLines*4),
	)
	return textResult(strings.Join(truncated, "\n") + banner)
}

// errorResult returns an error response
func errorResult(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: msg}},
		IsError: true,
	}
}

func NewMCPServer(cfg *config.Config, log *logrus.Logger) (*MCPServer, error) {
	if cfg == nil {
		return nil, errors.New("config is required")
	}
	if log == nil {
		log = logrus.New()
	}

	serverVersion := cfg.ServerVersion
	if serverVersion == "" {
		serverVersion = "dev"
	}
	s := mcp.NewServer(
		&mcp.Implementation{Name: "github-actions-mcp", Version: serverVersion},
		&mcp.ServerOptions{
			Instructions: "Use read-only tools to inspect GitHub Actions. Use manage_run or download_artifact only when the caller explicitly requests a mutation or local file write. Prefer get_run with element=info before fetching logs or artifacts.",
			PageSize:     25,
		},
	)

	github.SetLogger(log)

	// Use configured per-page limit or default to 50
	perPageLimit := cfg.PerPageLimit
	if perPageLimit <= 0 {
		perPageLimit = 50
	}

	ghClient, err := github.NewClientWithOptions(github.ClientOptions{
		Token:        cfg.Token,
		Owner:        cfg.RepoOwner,
		Repo:         cfg.RepoName,
		PerPageLimit: perPageLimit,
		APIBaseURL:   cfg.APIBaseURL,
		UploadURL:    cfg.UploadURL,
		RetryMax:     cfg.RetryMax,
		AuthUsername: cfg.AuthUsername,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create GitHub client: %w", err)
	}

	mcpServer := &MCPServer{
		srv:     s,
		client:  ghClient,
		config:  cfg,
		log:     log,
		version: serverVersion,
	}

	mcpServer.registerTools()
	mcpServer.registerResources()

	return mcpServer, nil
}

func (s *MCPServer) registerTools() {
	mcp := toolBuilder{}
	// Tool: list_workflows
	s.addTool(mcp.NewTool("list_workflows",
		mcp.WithDescription("List all workflows available in the repository"),
		mcp.ReadOnly(),
		mcp.WithString("owner",
			mcp.Description("Optional: override repository owner for this call"),
		),
		mcp.WithString("repo",
			mcp.Description("Optional: override repository name for this call"),
		),
		mcp.WithNumber("limit",
			mcp.Description("Maximum number of workflows to return (default: 5)"),
			mcp.DefaultNumber(5),
			mcp.Minimum(1),
			mcp.Maximum(100),
		),
		mcp.WithString("format",
			mcp.Description("Output format: compact (default, single-line JSON), pretty (indented JSON), or full (detailed)"),
			mcp.DefaultString("compact"),
		),
		mcp.WithString("cursor",
			mcp.Description("Opaque cursor returned by the previous call"),
		),
	))

	// Tool: list_runs
	s.addTool(mcp.NewTool("list_runs",
		mcp.WithDescription("List workflow runs with comprehensive filtering options"),
		mcp.ReadOnly(),
		mcp.WithString("owner",
			mcp.Description("Optional: override repository owner for this call"),
		),
		mcp.WithString("repo",
			mcp.Description("Optional: override repository name for this call"),
		),
		mcp.WithNumber("workflow_id",
			mcp.Description("Optional: The workflow ID or name (e.g., '12345678' or 'CI') to filter by"),
		),
		mcp.WithString("branch",
			mcp.Description("Optional: Branch to filter by. When omitted, runs from all branches are included."),
		),
		mcp.WithString("status",
			mcp.Description("Optional: Status to filter by (queued, in_progress, completed, etc.)"),
		),
		mcp.WithString("conclusion",
			mcp.Description("Optional: Conclusion to filter by (success, failure, neutral, cancelled, etc.)"),
		),
		mcp.WithNumber("per_page",
			mcp.Description("Number of results per page (default: 5)"),
			mcp.DefaultNumber(5),
			mcp.Minimum(1),
			mcp.Maximum(100),
		),
		mcp.WithString("created_after",
			mcp.Description("Optional: ISO 8601 date string to filter runs created after this time"),
		),
		mcp.WithString("event",
			mcp.Description("Optional: Event to filter by (push, pull_request, etc.)"),
		),
		mcp.WithString("actor",
			mcp.Description("Optional: GitHub username to filter by"),
		),
		mcp.WithString("format",
			mcp.Description("Output format: minimal (basic fields), compact (default, most fields), or full (all fields)"),
			mcp.DefaultString("compact"),
		),
		mcp.WithString("cursor",
			mcp.Description("Opaque cursor returned by the previous call"),
		),
	))

	// Tool: get_run
	s.addTool(mcp.NewTool("get_run",
		mcp.WithDescription("Get workflow run details. Start with element=info, then use jobs/logs/log_sections/artifacts as needed."),
		mcp.ReadOnly(),
		mcp.WithString("owner",
			mcp.Description("Optional: override repository owner for this call"),
		),
		mcp.WithString("repo",
			mcp.Description("Optional: override repository name for this call"),
		),
		mcp.WithNumber("run_id",
			mcp.Description("The workflow run ID"),
			mcp.Required(),
		),
		mcp.WithString("element",
			mcp.Description("Element to retrieve: info (default), jobs, logs, log_files, log_sections, artifacts, artifact_content. Invalid values return a validation error with allowed options."),
			mcp.DefaultString("info"),
			mcp.Enum("info", "jobs", "logs", "log_files", "log_sections", "artifacts", "artifact_content"),
		),
		mcp.WithNumber("artifact_id",
			mcp.Description("For element=artifact_content: the artifact ID to get contents for"),
		),
		mcp.WithString("file_pattern",
			mcp.Description("For element=logs or artifact_content: glob pattern to filter files (e.g., '*.log', 'build/*')"),
		),
		mcp.WithNumber("max_file_size",
			mcp.Description("For element=artifact_content: maximum size of individual files to read in bytes (default: 1MB)"),
			mcp.DefaultNumber(1024*1024),
		),
		mcp.WithNumber("job_id",
			mcp.Description("For element=logs or element=log_sections: specific job ID to get logs/sections for"),
		),
		mcp.WithBoolean("per_job",
			mcp.Description("For element=logs: get logs per-job instead of all logs combined"),
		),
		mcp.WithNumber("attempt_number",
			mcp.Description("For element=jobs: attempt number for the jobs (default: latest)"),
		),
		mcp.WithNumber("head",
			mcp.Description("For element=logs: return the first N lines of logs. Without head or tail, logs are auto-truncated to the last ~100 lines"),
		),
		mcp.WithNumber("tail",
			mcp.Description("For element=logs: return the last N lines of logs (default: auto-truncated to last ~100 lines if neither head nor tail is specified)"),
		),
		mcp.WithNumber("offset",
			mcp.Description("For element=logs: skip first N lines before returning (0-based)"),
		),
		mcp.WithString("search",
			mcp.Description("For element=logs: search/filter logs to lines containing this substring (case-insensitive)"),
		),
		mcp.WithString("search_regex",
			mcp.Description("For element=logs: filter logs to lines matching this regex pattern"),
		),
		mcp.WithNumber("context",
			mcp.Description("For element=logs: number of lines to show before and after each search match (default: 0)"),
			mcp.DefaultNumber(0),
		),
		mcp.WithBoolean("no_headers",
			mcp.Description("For element=logs: don't print file headers (=== filename ===)"),
		),
		mcp.WithString("section",
			mcp.Description("For element=logs: extract a specific section by name/pattern (e.g., 'Build', 'Test'). GitHub Actions sections are marked with ##[group]Section Name"),
		),
		mcp.WithString("format",
			mcp.Description("For element=info, jobs, artifacts, log_files: output format (compact/full, default: compact)"),
			mcp.DefaultString("compact"),
		),
	))

	// Tool: analyze_timing
	s.addTool(mcp.NewTool("analyze_timing",
		mcp.WithDescription("Analyze workflow, job, or step durations across recent runs to compare a specific CI run against recent history and surface slow spots."),
		mcp.ReadOnly(),
		mcp.WithString("owner",
			mcp.Description("Optional: override repository owner for this call"),
		),
		mcp.WithString("repo",
			mcp.Description("Optional: override repository name for this call"),
		),
		mcp.WithString("workflow",
			mcp.Description("Workflow selector (name, path, or numeric ID). Required unless run_id is provided."),
		),
		mcp.WithNumber("run_id",
			mcp.Description("Optional: focus on a specific workflow run ID. When omitted, the latest matching run is used."),
		),
		mcp.WithString("branch",
			mcp.Description("Optional: branch to compare against. When omitted, compares against runs from all branches."),
		),
		mcp.WithString("job_name",
			mcp.Description("Optional: analyze a specific job across runs. Required when step_name is provided."),
		),
		mcp.WithString("step_name",
			mcp.Description("Optional: analyze a specific step within job_name across runs."),
		),
		mcp.WithString("conclusion",
			mcp.Description("Optional: only include runs with a specific conclusion (success, failure, cancelled, etc.)."),
		),
		mcp.WithNumber("limit",
			mcp.Description("Maximum number of recent runs to analyze (default: 10, max: 50)."),
			mcp.DefaultNumber(10),
			mcp.Minimum(1),
			mcp.Maximum(50),
		),
	))

	// Tool: get_check_status
	s.addTool(mcp.NewTool("get_check_status",
		mcp.WithDescription("Get workflow status summary for a commit/branch/tag (derived from workflow runs; no Checks API permission required)."),
		mcp.ReadOnly(),
		mcp.WithString("owner",
			mcp.Description("Optional: override repository owner for this call"),
		),
		mcp.WithString("repo",
			mcp.Description("Optional: override repository name for this call"),
		),
		mcp.WithString("ref",
			mcp.Description("Git ref (commit SHA, branch name, or tag) - default: HEAD of current branch"),
		),
		mcp.WithString("check_name",
			mcp.Description("Optional: filter by specific check name"),
		),
		mcp.WithString("status",
			mcp.Description("Optional: filter by status (queued, in_progress, completed)"),
		),
		mcp.WithString("filter",
			mcp.Description("Return latest workflow statuses (default) or all statuses for the ref. Allowed: latest, all."),
			mcp.DefaultString("latest"),
			mcp.Enum("latest", "all"),
		),
		mcp.WithString("format",
			mcp.Description("Output format: summary (default), compact, or full"),
			mcp.DefaultString("summary"),
		),
	))

	// Tool: wait_for_run
	s.addTool(mcp.NewTool("wait_for_run",
		mcp.WithDescription("Wait silently for a workflow run to complete (no output during polling)"),
		mcp.ReadOnly(),
		mcp.WithString("owner",
			mcp.Description("Optional: override repository owner for this call"),
		),
		mcp.WithString("repo",
			mcp.Description("Optional: override repository name for this call"),
		),
		mcp.WithNumber("run_id",
			mcp.Description("The workflow run ID to wait for"),
			mcp.Required(),
		),
		mcp.WithNumber("timeout_minutes",
			mcp.Description("Maximum time to wait in minutes (default: 30)"),
			mcp.DefaultNumber(30),
			mcp.Minimum(1),
			mcp.Maximum(120),
		),
	))

	// Tool: wait_all
	s.addTool(mcp.NewTool("wait_all",
		mcp.WithDescription("Wait silently for every job in a workflow run to complete, regardless of job status"),
		mcp.ReadOnly(),
		mcp.WithString("owner",
			mcp.Description("Optional: override repository owner for this call"),
		),
		mcp.WithString("repo",
			mcp.Description("Optional: override repository name for this call"),
		),
		mcp.WithNumber("run_id",
			mcp.Description("The workflow run ID to wait for"),
			mcp.Required(),
		),
		mcp.WithNumber("timeout_minutes",
			mcp.Description("Maximum time to wait in minutes (default: 30)"),
			mcp.DefaultNumber(30),
			mcp.Minimum(1),
			mcp.Maximum(120),
		),
	))

	// Tool: wait_for_commit_checks
	s.addTool(mcp.NewTool("wait_for_commit_checks",
		mcp.WithDescription("Wait for all CI check runs for a commit ref (SHA, branch, or tag) to complete."),
		mcp.ReadOnly(),
		mcp.WithString("owner",
			mcp.Description("Optional: override repository owner for this call"),
		),
		mcp.WithString("repo",
			mcp.Description("Optional: override repository name for this call"),
		),
		mcp.WithString("ref",
			mcp.Description("Git ref (commit SHA, branch name, or tag) - default: HEAD"),
		),
		mcp.WithNumber("timeout_minutes",
			mcp.Description("Maximum time to wait in minutes (default: 30)"),
			mcp.DefaultNumber(30),
			mcp.Minimum(1),
			mcp.Maximum(120),
		),
	))

	// Tool: manage_run
	s.addTool(mcp.NewTool("manage_run",
		mcp.WithDescription("Manage a workflow run (cancel, rerun, or rerun failed jobs)"),
		mcp.Destructive(),
		mcp.WithString("owner",
			mcp.Description("Optional: override repository owner for this call"),
		),
		mcp.WithString("repo",
			mcp.Description("Optional: override repository name for this call"),
		),
		mcp.WithNumber("run_id",
			mcp.Description("The workflow run ID to manage"),
			mcp.Required(),
		),
		mcp.WithString("action",
			mcp.Description("Action to perform: cancel, rerun, or rerun_failed"),
			mcp.Required(),
			mcp.Enum("cancel", "rerun", "rerun_failed"),
		),
	))

	// Tool: get_artifact
	s.addTool(mcp.NewTool("get_artifact",
		mcp.WithDescription("Get the contents of a workflow run artifact (stream without downloading to disk)"),
		mcp.ReadOnly(),
		mcp.WithString("owner",
			mcp.Description("Optional: override repository owner for this call"),
		),
		mcp.WithString("repo",
			mcp.Description("Optional: override repository name for this call"),
		),
		mcp.WithNumber("artifact_id",
			mcp.Description("The artifact ID"),
			mcp.Required(),
		),
		mcp.WithString("file_pattern",
			mcp.Description("Optional: glob pattern to filter files within the artifact (e.g., '*.txt', 'logs/*.log')"),
		),
		mcp.WithNumber("max_file_size",
			mcp.Description("Optional: maximum size of individual files to read in bytes (default: 1MB). Files larger than this will show size info only."),
			mcp.DefaultNumber(1024*1024),
		),
	))

	// Tool: diagnose_failure
	s.addTool(mcp.NewTool("diagnose_failure",
		mcp.WithDescription("One-shot diagnosis of a failed workflow run: identifies failed jobs/steps, extracts error lines from logs, and optionally checks for flakiness. Returns a structured diagnosis with actionable error context."),
		mcp.ReadOnly(),
		mcp.WithString("owner",
			mcp.Description("Optional: override repository owner for this call"),
		),
		mcp.WithString("repo",
			mcp.Description("Optional: override repository name for this call"),
		),
		mcp.WithNumber("run_id",
			mcp.Description("The workflow run ID to diagnose. If omitted, diagnoses the latest failed run on the current branch."),
		),
		mcp.WithBoolean("check_flakiness",
			mcp.Description("Compare against recent runs to detect flaky tests (default: true). Adds a few extra API calls."),
		),
		mcp.WithNumber("max_error_lines",
			mcp.Description("Maximum number of error lines to extract per job (default: 50)"),
			mcp.DefaultNumber(50),
		),
	))

	// Tool: download_artifact
	s.addTool(mcp.NewTool("download_artifact",
		mcp.WithDescription("Download a workflow run artifact to disk"),
		mcp.Destructive(),
		mcp.WithString("owner",
			mcp.Description("Optional: override repository owner for this call"),
		),
		mcp.WithString("repo",
			mcp.Description("Optional: override repository name for this call"),
		),
		mcp.WithNumber("artifact_id",
			mcp.Description("The artifact ID"),
			mcp.Required(),
		),
		mcp.WithString("output_path",
			mcp.Description("Optional path relative to artifact_root (default: {artifact-name}.zip)"),
		),
		mcp.WithBoolean("overwrite",
			mcp.Description("Replace an existing destination atomically (default: false)"),
		),
	))
}

func (s *MCPServer) GetServer() *mcp.Server {
	return s.srv
}

// InvokeTool executes a tool through an official SDK client session. This
// keeps the local CLI path identical to calls arriving over MCP transports.
func (s *MCPServer) InvokeTool(ctx context.Context, name string, args map[string]interface{}) (*mcp.CallToolResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if args == nil {
		args = map[string]interface{}{}
	}

	session, err := s.invokeClientSession()
	if err != nil {
		return nil, err
	}
	return session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
}

func (s *MCPServer) invokeClientSession() (*mcp.ClientSession, error) {
	s.invokeMu.Lock()
	defer s.invokeMu.Unlock()
	if s.invokeSession != nil {
		return s.invokeSession, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := s.srv.Connect(ctx, serverTransport, nil)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to connect local MCP server: %w", err)
	}

	client := mcp.NewClient(&mcp.Implementation{
		Name:    "gh-actions-mcp-cli",
		Version: s.version,
	}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		_ = serverSession.Close()
		cancel()
		return nil, fmt.Errorf("failed to connect local MCP client: %w", err)
	}

	s.invokeCancel = cancel
	s.invokeServerSession = serverSession
	s.invokeSession = clientSession
	return clientSession, nil
}

// Close releases the lazy in-memory MCP session, if one was created.
func (s *MCPServer) Close() error {
	s.invokeMu.Lock()
	defer s.invokeMu.Unlock()

	var firstErr error
	if s.invokeSession != nil {
		firstErr = s.invokeSession.Close()
		s.invokeSession = nil
	}
	if s.invokeServerSession != nil {
		if err := s.invokeServerSession.Close(); firstErr == nil {
			firstErr = err
		}
		s.invokeServerSession = nil
	}
	if s.invokeCancel != nil {
		s.invokeCancel()
		s.invokeCancel = nil
	}
	return firstErr
}
