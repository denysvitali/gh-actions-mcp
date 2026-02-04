package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/denysvitali/gh-actions-mcp/config"
	"github.com/denysvitali/gh-actions-mcp/github"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/sirupsen/logrus"
)

type MCPServer struct {
	srv    *server.MCPServer
	client *github.Client
	config *config.Config
	log    *logrus.Logger
}

// Default limits for output control
const (
	DefaultListLimit = 5   // Default max items for lists (reduced from 10 for token efficiency)
	DefaultLogLines  = 50  // Default max lines for logs (reduced from 100 for token efficiency)
)

// Helper functions to reduce repetition

// getLimit returns the limit from config or default
func (s *MCPServer) getLimit() int {
	if s.config.DefaultLimit > 0 {
		return s.config.DefaultLimit
	}
	return DefaultListLimit
}

// getLogLimit returns the log limit from config or default
func (s *MCPServer) getLogLimit() int {
	if s.config.DefaultLogLen > 0 {
		return s.config.DefaultLogLen
	}
	return DefaultLogLines
}

// formatAuthError formats an error message with authentication context
func (s *MCPServer) formatAuthError(err error, msg string) string {
	if config.IsAuthenticationError(err) {
		return fmt.Sprintf("authentication failed: %v\nMake sure GITHUB_TOKEN is set (or run 'gh auth login' on macOS) and has access to %s/%s", err, s.config.RepoOwner, s.config.RepoName)
	}
	return fmt.Sprintf("%s: %v", msg, err)
}

// jsonResult returns a successful JSON response
func jsonResult(data interface{}) (*mcp.CallToolResult, error) {
	d, err := json.Marshal(data)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to marshal: %v", err)), nil
	}
	return mcp.NewToolResultText(string(d)), nil
}

// jsonResultPretty returns a successful JSON response with pretty formatting
func jsonResultPretty(data interface{}) (*mcp.CallToolResult, error) {
	d, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to marshal: %v", err)), nil
	}
	return mcp.NewToolResultText(string(d)), nil
}

// textResult returns a simple text response
func textResult(msg string) *mcp.CallToolResult {
	return mcp.NewToolResultText(msg)
}

// errorResult returns an error response
func errorResult(msg string) *mcp.CallToolResult {
	return mcp.NewToolResultError(msg)
}

// extractRunID extracts run_id from arguments, returning (runID, error)
func extractRunID(arguments map[string]interface{}) (int64, bool) {
	runIDFloat, ok := arguments["run_id"].(float64)
	if !ok {
		return 0, false
	}
	return int64(runIDFloat), true
}

func NewMCPServer(cfg *config.Config, log *logrus.Logger) *MCPServer {
	s := server.NewMCPServer(
		"github-actions-mcp",
		"Get GitHub Actions status and manage workflow runs",
		server.WithToolCapabilities(true),
	)

	github.SetLogger(log)

	// Use configured per-page limit or default to 50
	perPageLimit := cfg.PerPageLimit
	if perPageLimit <= 0 {
		perPageLimit = 50
	}

	ghClient := github.NewClientWithPerPage(cfg.Token, cfg.RepoOwner, cfg.RepoName, perPageLimit)

	mcpServer := &MCPServer{
		srv:    s,
		client: ghClient,
		config: cfg,
		log:    log,
	}

	mcpServer.registerTools()

	return mcpServer
}

func (s *MCPServer) registerTools() {
	// Tool: list_workflows
	s.srv.AddTool(mcp.NewTool("list_workflows",
		mcp.WithDescription("List all workflows available in the repository"),
		mcp.WithNumber("limit",
			mcp.Description("Maximum number of workflows to return (default: 5)"),
			mcp.DefaultNumber(5),
		),
		mcp.WithString("format",
			mcp.Description("Output format: compact (default, single-line JSON), pretty (indented JSON), or full (detailed)"),
			mcp.DefaultString("compact"),
		),
	), s.listWorkflows)

	// Tool: list_runs
	s.srv.AddTool(mcp.NewTool("list_runs",
		mcp.WithDescription("List workflow runs with comprehensive filtering options"),
		mcp.WithNumber("workflow_id",
			mcp.Description("Optional: The workflow ID or name (e.g., '12345678' or 'CI') to filter by"),
		),
		mcp.WithString("branch",
			mcp.Description("Optional: Branch to filter by (default: auto-detect from git repository)"),
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
	), s.listRuns)

	// Tool: get_run
	s.srv.AddTool(mcp.NewTool("get_run",
		mcp.WithDescription("Get detailed information about a workflow run, including jobs, logs, artifacts, or artifact content"),
		mcp.WithNumber("run_id",
			mcp.Description("The workflow run ID"),
			mcp.Required(),
		),
		mcp.WithString("element",
			mcp.Description("Element to retrieve: info (default), jobs, logs, log_files, log_sections, artifacts, artifact_content"),
			mcp.DefaultString("info"),
		),
		mcp.WithNumber("artifact_id",
			mcp.Description("For element=artifact_content: the artifact ID to get contents for"),
		),
		mcp.WithString("file_pattern",
			mcp.Description("For element=logs or artifact_content: glob pattern to filter files (e.g., '*.log', 'build/*')"),
		),
		mcp.WithNumber("max_file_size",
			mcp.Description("For element=artifact_content: maximum size of individual files to read in bytes (default: 1MB)"),
			mcp.DefaultNumber(1024 * 1024),
		),
		mcp.WithNumber("job_id",
			mcp.Description("For element=logs: specific job ID to get logs for"),
		),
		mcp.WithBoolean("per_job",
			mcp.Description("For element=logs: get logs per-job instead of all logs combined"),
		),
		mcp.WithNumber("attempt_number",
			mcp.Description("For element=jobs: attempt number for the jobs (default: latest)"),
		),
		mcp.WithNumber("head",
			mcp.Description("For element=logs: return the first N lines of logs"),
		),
		mcp.WithNumber("tail",
			mcp.Description("For element=logs: return the last N lines of logs"),
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
	), s.getRun)

	// Tool: get_check_status
	s.srv.AddTool(mcp.NewTool("get_check_status",
		mcp.WithDescription("Get check run status for a specific commit/branch/tag"),
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
			mcp.Description("Return latest check runs (default) or all check runs for the ref"),
			mcp.DefaultString("latest"),
		),
		mcp.WithString("format",
			mcp.Description("Output format: summary (default), compact, or full"),
			mcp.DefaultString("summary"),
		),
	), s.getCheckStatus)

	// Tool: wait_for_run
	s.srv.AddTool(mcp.NewTool("wait_for_run",
		mcp.WithDescription("Wait silently for a workflow run to complete (no output during polling)"),
		mcp.WithNumber("run_id",
			mcp.Description("The workflow run ID to wait for"),
			mcp.Required(),
		),
		mcp.WithNumber("timeout_minutes",
			mcp.Description("Maximum time to wait in minutes (default: 30)"),
			mcp.DefaultNumber(30),
		),
	), s.waitForRun)

	// Tool: wait_for_commit_checks
	s.srv.AddTool(mcp.NewTool("wait_for_commit_checks",
		mcp.WithDescription("Wait for all check runs for a commit to complete"),
		mcp.WithString("ref",
			mcp.Description("Git ref (commit SHA, branch name, or tag) - default: HEAD"),
		),
		mcp.WithNumber("timeout_minutes",
			mcp.Description("Maximum time to wait in minutes (default: 30)"),
			mcp.DefaultNumber(30),
		),
	), s.waitForCommitChecks)

	// Tool: manage_run
	s.srv.AddTool(mcp.NewTool("manage_run",
		mcp.WithDescription("Manage a workflow run (cancel, rerun, or rerun failed jobs)"),
		mcp.WithNumber("run_id",
			mcp.Description("The workflow run ID to manage"),
			mcp.Required(),
		),
		mcp.WithString("action",
			mcp.Description("Action to perform: cancel, rerun, or rerun_failed"),
			mcp.Required(),
		),
	), s.manageRun)

	// Tool: get_artifact
	s.srv.AddTool(mcp.NewTool("get_artifact",
		mcp.WithDescription("Get the contents of a workflow run artifact (stream without downloading to disk)"),
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
	), s.getArtifact)

	// Tool: download_artifact
	s.srv.AddTool(mcp.NewTool("download_artifact",
		mcp.WithDescription("Download a workflow run artifact to disk"),
		mcp.WithNumber("artifact_id",
			mcp.Description("The artifact ID"),
			mcp.Required(),
		),
		mcp.WithString("output_path",
			mcp.Description("Optional: path where to save the artifact (default: {artifact-name}.zip)"),
		),
	), s.downloadArtifact)
}

func (s *MCPServer) listWorkflows(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	limit := s.getLimit()

	if l, ok := request.GetArguments()["limit"]; ok {
		if n, err := strconv.Atoi(fmt.Sprintf("%.0f", l)); err == nil {
			limit = n
		}
	}

	format := s.getFormat()
	if f, ok := request.GetArguments()["format"].(string); ok {
		format = f
	}

	s.log.Infof("Listing workflows for %s/%s (limit: %d, format: %s)", s.config.RepoOwner, s.config.RepoName, limit, format)

	workflows, err := s.client.GetWorkflows(ctx)
	if err != nil {
		return errorResult(s.formatAuthError(err, "failed to list workflows")), nil
	}

	// Apply limit
	result := workflows[:0]
	for _, w := range workflows {
		if len(result) >= limit {
			break
		}
		result = append(result, w)
	}

	switch format {
	case "pretty", "full":
		return jsonResultPretty(result)
	default:
		return jsonResult(result)
	}
}

func (s *MCPServer) listRuns(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := request.GetArguments()

	// Build options
	opts := &github.ListRunsOptions{
		Per_page: s.getLimit(),
	}

	if perPage, ok := args["per_page"].(float64); ok && perPage > 0 {
		opts.Per_page = int(perPage)
	}

	if workflowIDStr, ok := args["workflow_id"].(string); ok && workflowIDStr != "" {
		if workflowID, _, err := s.client.ResolveWorkflowID(ctx, workflowIDStr); err == nil {
			opts.WorkflowID = &workflowID
		}
	}

	if branch, ok := args["branch"].(string); ok && branch != "" {
		opts.Branch = branch
	} else {
		// Auto-detect branch
		if detectedBranch, err := github.GetCurrentBranch(); err == nil {
			opts.Branch = detectedBranch
			s.log.Debugf("Auto-detected branch: %s", detectedBranch)
		}
	}

	if status, ok := args["status"].(string); ok && status != "" {
		opts.Status = status
	}

	if conclusion, ok := args["conclusion"].(string); ok && conclusion != "" {
		opts.Conclusion = conclusion
	}

	if createdAfter, ok := args["created_after"].(string); ok && createdAfter != "" {
		opts.CreatedAfter = createdAfter
	}

	if event, ok := args["event"].(string); ok && event != "" {
		opts.Event = event
	}

	if actor, ok := args["actor"].(string); ok && actor != "" {
		opts.Actor = actor
	}

	format := s.getFormat()
	if f, ok := args["format"].(string); ok {
		format = f
	}

	s.log.Infof("Listing runs for %s/%s", s.config.RepoOwner, s.config.RepoName)

	runs, err := s.client.ListRepositoryWorkflowRunsWithOptions(ctx, opts)
	if err != nil {
		return errorResult(s.formatAuthError(err, "failed to list workflow runs")), nil
	}

	// Format output based on format parameter
	switch format {
	case "minimal":
		result := make([]*github.WorkflowRunMinimal, 0, len(runs))
		for _, r := range runs {
			result = append(result, &github.WorkflowRunMinimal{
				ID:         r.ID,
				Name:       r.Name,
				Status:     r.Status,
				Conclusion: r.Conclusion,
				CreatedAt:  r.CreatedAt,
			})
		}
		return jsonResult(result)
	case "full":
		result := make([]*github.WorkflowRunFull, 0, len(runs))
		for _, r := range runs {
			result = append(result, &github.WorkflowRunFull{
				ID:          r.ID,
				Name:        r.Name,
				Status:      r.Status,
				Conclusion:  r.Conclusion,
				Branch:      r.Branch,
				Event:       r.Event,
				Actor:       r.Actor,
				CreatedAt:   r.CreatedAt,
				UpdatedAt:   r.UpdatedAt,
				URL:         r.URL,
				RunNumber:   r.RunNumber,
				WorkflowID:  r.WorkflowID,
				HeadSHA:     "",
			})
		}
		return jsonResult(result)
	default: // compact
		result := make([]*github.WorkflowRunCompact, 0, len(runs))
		for _, r := range runs {
			result = append(result, &github.WorkflowRunCompact{
				WorkflowRunMinimal: github.WorkflowRunMinimal{
					ID:         r.ID,
					Name:       r.Name,
					Status:     r.Status,
					Conclusion: r.Conclusion,
					CreatedAt:  r.CreatedAt,
				},
				Branch: r.Branch,
				SHA:    "",
				Event:  r.Event,
				Actor:  r.Actor,
				URL:    r.URL,
			})
		}
		return jsonResult(result)
	}
}

func (s *MCPServer) getRun(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := request.GetArguments()

	runID, ok := extractRunID(args)
	if !ok {
		return errorResult("run_id is required"), nil
	}

	element := "info"
	if e, ok := args["element"].(string); ok {
		element = e
	}

	s.log.Infof("Getting run %d (element: %s)", runID, element)

	switch element {
	case "jobs":
		return s.getRunJobs(ctx, runID, args)
	case "logs":
		return s.getRunLogs(ctx, runID, args)
	case "log_files":
		return s.getLogFiles(ctx, runID, args)
	case "log_sections":
		return s.getLogSections(ctx, runID, args)
	case "artifacts":
		return s.getRunArtifacts(ctx, runID, args)
	case "artifact_content":
		return s.getArtifactContent(ctx, args)
	default: // info
		return s.getRunInfo(ctx, runID, args)
	}
}

func (s *MCPServer) getRunInfo(ctx context.Context, runID int64, args map[string]interface{}) (*mcp.CallToolResult, error) {
	run, err := s.client.GetWorkflowRun(ctx, runID)
	if err != nil {
		return errorResult(s.formatAuthError(err, fmt.Sprintf("Run ID %d not found", runID))), nil
	}

	format := s.getFormat()
	if f, ok := args["format"].(string); ok {
		format = f
	}

	switch format {
	case "full":
		result := &github.WorkflowRunFull{
			ID:          run.ID,
			Name:        run.Name,
			Status:      run.Status,
			Conclusion:  run.Conclusion,
			Branch:      run.Branch,
			Event:       run.Event,
			Actor:       run.Actor,
			CreatedAt:   run.CreatedAt,
			UpdatedAt:   run.UpdatedAt,
			URL:         run.URL,
			RunNumber:   run.RunNumber,
			WorkflowID:  run.WorkflowID,
			HeadSHA:     "",
		}
		return jsonResult(result)
	default: // compact
		result := &github.WorkflowRunCompact{
			WorkflowRunMinimal: github.WorkflowRunMinimal{
				ID:         run.ID,
				Name:       run.Name,
				Status:     run.Status,
				Conclusion: run.Conclusion,
				CreatedAt:  run.CreatedAt,
			},
			Branch: run.Branch,
			SHA:    "",
			Event:  run.Event,
			Actor:  run.Actor,
			URL:    run.URL,
		}
		return jsonResult(result)
	}
}

func (s *MCPServer) getRunJobs(ctx context.Context, runID int64, args map[string]interface{}) (*mcp.CallToolResult, error) {
	filter := ""
	if f, ok := args["filter"].(string); ok {
		filter = f
	}

	attemptNumber := 0
	if an, ok := args["attempt_number"].(float64); ok && an > 0 {
		attemptNumber = int(an)
	}

	jobs, err := s.client.GetWorkflowJobs(ctx, runID, filter, attemptNumber)
	if err != nil {
		return errorResult(s.formatAuthError(err, fmt.Sprintf("failed to get jobs for run %d", runID))), nil
	}

	format := s.getFormat()
	if f, ok := args["format"].(string); ok {
		format = f
	}

	if format == "full" {
		return jsonResult(jobs)
	}
	return jsonResult(jobs)
}

func (s *MCPServer) getRunLogs(ctx context.Context, runID int64, args map[string]interface{}) (*mcp.CallToolResult, error) {
	// Check if getting logs for a specific job
	if jobIDFloat, ok := args["job_id"].(float64); ok {
		jobID := int64(jobIDFloat)
		return s.getJobLogs(ctx, jobID, args)
	}

	// Get workflow run logs
	head := 0
	if h, ok := args["head"].(float64); ok && h > 0 {
		head = int(h)
	}

	tail := 0
	if t, ok := args["tail"].(float64); ok && t > 0 {
		tail = int(t)
	}

	offset := 0
	if o, ok := args["offset"].(float64); ok && o > 0 {
		offset = int(o)
	}

	// Support both old 'filter' and new 'search' parameter
	search := ""
	if s, ok := args["search"].(string); ok {
		search = s
	} else if f, ok := args["filter"].(string); ok {
		search = f // Support old parameter name for backwards compatibility
	}

	searchRegex := ""
	if sr, ok := args["search_regex"].(string); ok {
		searchRegex = sr
	} else if fr, ok := args["filter_regex"].(string); ok {
		searchRegex = fr // Support old parameter name
	}

	if search != "" && searchRegex != "" {
		return errorResult("search and search_regex are mutually exclusive"), nil
	}

	contextLines := 0
	if c, ok := args["context"].(float64); ok && c > 0 {
		contextLines = int(c)
	}

	noHeaders := false
	if nh, ok := args["no_headers"].(bool); ok {
		noHeaders = nh
	}

	// Get file pattern for filtering log files
	filePattern := ""
	if fp, ok := args["file_pattern"].(string); ok {
		filePattern = fp
	}

	filterOpts := &github.LogFilterOptions{
		Filter:       search,
		FilterRegex:  searchRegex,
		ContextLines: contextLines,
	}

	// Check if section extraction is requested
	section := ""
	if sec, ok := args["section"].(string); ok {
		section = sec
	}

	var logs string
	var err error

	if section != "" {
		// Extract specific section
		logs, err = s.client.GetLogSection(ctx, runID, 0, section, filterOpts)
	} else {
		// Get all logs with optional filtering
		logs, err = s.client.GetWorkflowLogsWithPattern(ctx, runID, head, tail, offset, noHeaders, filePattern, filterOpts)
	}

	if err != nil {
		return errorResult(s.formatAuthError(err, fmt.Sprintf("failed to get logs for run %d", runID))), nil
	}

	return textResult(logs), nil
}

func (s *MCPServer) getJobLogs(ctx context.Context, jobID int64, args map[string]interface{}) (*mcp.CallToolResult, error) {
	head := 0
	if h, ok := args["head"].(float64); ok && h > 0 {
		head = int(h)
	}

	tail := 0
	if t, ok := args["tail"].(float64); ok && t > 0 {
		tail = int(t)
	}

	offset := 0
	if o, ok := args["offset"].(float64); ok && o > 0 {
		offset = int(o)
	}

	// Support both old 'filter' and new 'search' parameter
	search := ""
	if s, ok := args["search"].(string); ok {
		search = s
	} else if f, ok := args["filter"].(string); ok {
		search = f
	}

	searchRegex := ""
	if sr, ok := args["search_regex"].(string); ok {
		searchRegex = sr
	} else if fr, ok := args["filter_regex"].(string); ok {
		searchRegex = fr
	}

	if search != "" && searchRegex != "" {
		return errorResult("search and search_regex are mutually exclusive"), nil
	}

	contextLines := 0
	if c, ok := args["context"].(float64); ok && c > 0 {
		contextLines = int(c)
	}

	noHeaders := false
	if nh, ok := args["no_headers"].(bool); ok {
		noHeaders = nh
	}

	filterOpts := &github.LogFilterOptions{
		Filter:       search,
		FilterRegex:  searchRegex,
		ContextLines: contextLines,
	}

	// Check if section extraction is requested
	section := ""
	if sec, ok := args["section"].(string); ok {
		section = sec
	}

	var logs string
	var err error

	if section != "" {
		// Extract specific section from job logs
		// Note: GetLogSection with jobID > 0 fetches job-specific logs
		logs, err = s.client.GetLogSection(ctx, 0, jobID, section, filterOpts)
	} else {
		// Get all job logs with optional filtering
		logs, err = s.client.GetWorkflowJobLogs(ctx, jobID, head, tail, offset, noHeaders, filterOpts)
	}

	if err != nil {
		return errorResult(s.formatAuthError(err, fmt.Sprintf("failed to get logs for job %d", jobID))), nil
	}

	return textResult(logs), nil
}

func (s *MCPServer) getRunArtifacts(ctx context.Context, runID int64, args map[string]interface{}) (*mcp.CallToolResult, error) {
	artifacts, err := s.client.GetWorkflowRunArtifacts(ctx, runID)
	if err != nil {
		return errorResult(s.formatAuthError(err, fmt.Sprintf("failed to get artifacts for run %d", runID))), nil
	}

	format := s.getFormat()
	if f, ok := args["format"].(string); ok {
		format = f
	}

	if format == "full" {
		return jsonResult(artifacts)
	}
	return jsonResult(artifacts)
}

func (s *MCPServer) getArtifactContent(ctx context.Context, args map[string]interface{}) (*mcp.CallToolResult, error) {
	artifactIDFloat, ok := args["artifact_id"].(float64)
	if !ok {
		return errorResult("artifact_id is required for element=artifact_content"), nil
	}
	artifactID := int64(artifactIDFloat)

	filePattern := ""
	if fp, ok := args["file_pattern"].(string); ok {
		filePattern = fp
	}

	maxFileSize := int64(1024 * 1024) // 1MB default
	if mfs, ok := args["max_file_size"].(float64); ok && mfs > 0 {
		maxFileSize = int64(mfs)
	}

	s.log.Infof("Getting artifact content %d (pattern: %s, max_size: %d)", artifactID, filePattern, maxFileSize)

	content, err := s.client.GetArtifactContent(ctx, artifactID, filePattern, maxFileSize)
	if err != nil {
		return errorResult(s.formatAuthError(err, fmt.Sprintf("failed to get artifact content %d", artifactID))), nil
	}

	return jsonResultPretty(content)
}

func (s *MCPServer) getLogFiles(ctx context.Context, runID int64, args map[string]interface{}) (*mcp.CallToolResult, error) {
	logFiles, err := s.client.GetWorkflowLogFiles(ctx, runID)
	if err != nil {
		return errorResult(s.formatAuthError(err, fmt.Sprintf("failed to get log files for run %d", runID))), nil
	}

	// Apply file pattern filter if specified
	if pattern, ok := args["file_pattern"].(string); ok && pattern != "" {
		filtered := make([]*github.LogFileInfo, 0)
		for _, lf := range logFiles {
			matched, err := filepath.Match(pattern, lf.Path)
			if err != nil {
				return errorResult(fmt.Sprintf("invalid file pattern %q: %v", pattern, err)), nil
			}
			if matched {
				filtered = append(filtered, lf)
			}
		}
		logFiles = filtered
	}

	format := s.getFormat()
	if f, ok := args["format"].(string); ok {
		format = f
	}

	if format == "full" {
		return jsonResultPretty(logFiles)
	}
	return jsonResult(logFiles)
}

func (s *MCPServer) getLogSections(ctx context.Context, runID int64, args map[string]interface{}) (*mcp.CallToolResult, error) {
	// Check if getting sections for a specific job
	var jobID int64
	if jobIDFloat, ok := args["job_id"].(float64); ok {
		jobID = int64(jobIDFloat)
	}

	s.log.Infof("Getting log sections for run %d (job_id: %d)", runID, jobID)

	sections, err := s.client.ListLogSections(ctx, runID, jobID)
	if err != nil {
		return errorResult(s.formatAuthError(err, fmt.Sprintf("failed to get log sections for run %d", runID))), nil
	}

	format := s.getFormat()
	if f, ok := args["format"].(string); ok {
		format = f
	}

	if format == "full" {
		return jsonResultPretty(sections)
	}
	return jsonResult(sections)
}

func (s *MCPServer) getCheckStatus(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := request.GetArguments()

	ref := ""
	if r, ok := args["ref"].(string); ok && r != "" {
		ref = strings.TrimSpace(r)
	} else {
		// Auto-detect ref (HEAD SHA)
		if commit, err := github.GetLastCommit(); err == nil {
			ref = commit.SHA
		} else {
			return errorResult("could not determine ref - please specify explicitly"), nil
		}
	}

	opts := &github.GetCheckRunsOptions{}

	if checkName, ok := args["check_name"].(string); ok && checkName != "" {
		opts.CheckName = checkName
	}

	if status, ok := args["status"].(string); ok && status != "" {
		opts.Status = status
	}

	if filter, ok := args["filter"].(string); ok && filter == "all" {
		opts.Filter = "all"
	} else {
		opts.Filter = "latest"
	}

	format := "summary"
	if f, ok := args["format"].(string); ok {
		format = f
	}

	status, err := s.client.GetCheckRunsForRef(ctx, ref, opts)
	if err != nil {
		return errorResult(s.formatAuthError(err, "failed to get check status")), nil
	}

	switch format {
	case "full":
		return jsonResult(status)
	case "compact":
		return jsonResult(status)
	default: // summary
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("Check Status for %s\n", ref))
		sb.WriteString(fmt.Sprintf("State: %s\n", status.State))
		sb.WriteString(fmt.Sprintf("Total Checks: %d\n", status.TotalCount))
		if len(status.ByConclusion) > 0 {
			sb.WriteString("By Conclusion:\n")
			for conclusion, count := range status.ByConclusion {
				sb.WriteString(fmt.Sprintf("  %s: %d\n", conclusion, count))
			}
		}
		return textResult(sb.String()), nil
	}
}

func (s *MCPServer) waitForRun(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := request.GetArguments()

	runID, ok := extractRunID(args)
	if !ok {
		return errorResult("run_id is required"), nil
	}

	timeoutMinutes := 30
	if tm, ok := args["timeout_minutes"].(float64); ok && tm > 0 {
		timeoutMinutes = int(tm)
	}

	s.log.Infof("Waiting for run %d (timeout: %dm)", runID, timeoutMinutes)

	result, err := s.client.WaitForRun(ctx, runID, timeoutMinutes)
	if err != nil && !result.TimeoutReached {
		return errorResult(s.formatAuthError(err, "failed to wait for run")), nil
	}

	return jsonResult(result)
}

func (s *MCPServer) waitForCommitChecks(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := request.GetArguments()

	ref := ""
	if r, ok := args["ref"].(string); ok && r != "" {
		ref = strings.TrimSpace(r)
	}

	timeoutMinutes := 30
	if tm, ok := args["timeout_minutes"].(float64); ok && tm > 0 {
		timeoutMinutes = int(tm)
	}

	s.log.Infof("Waiting for checks on ref %s (timeout: %dm)", ref, timeoutMinutes)

	result, err := s.client.WaitForCommitChecks(ctx, ref, timeoutMinutes)
	if err != nil && !result.TimeoutReached {
		return errorResult(s.formatAuthError(err, "failed to wait for checks")), nil
	}

	return jsonResult(result)
}

func (s *MCPServer) manageRun(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := request.GetArguments()

	runID, ok := extractRunID(args)
	if !ok {
		return errorResult("run_id is required"), nil
	}

	actionStr, ok := args["action"].(string)
	if !ok || actionStr == "" {
		return errorResult("action is required (cancel, rerun, rerun_failed)"), nil
	}

	var action github.ManageRunAction
	switch actionStr {
	case "cancel":
		action = github.ManageRunActionCancel
	case "rerun":
		action = github.ManageRunActionRerun
	case "rerun_failed":
		action = github.ManageRunActionRerunFailed
	default:
		return errorResult(fmt.Sprintf("unknown action: %s (must be cancel, rerun, or rerun_failed)", actionStr)), nil
	}

	s.log.Infof("Managing run %d: %s", runID, action)

	result, err := s.client.ManageRun(ctx, runID, action)
	if err != nil {
		return errorResult(s.formatAuthError(err, "failed to manage run")), nil
	}

	if result.Status == "success" {
		return textResult(result.Message), nil
	}
	return errorResult(result.Message), nil
}

func (s *MCPServer) getArtifact(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := request.GetArguments()

	artifactIDFloat, ok := args["artifact_id"].(float64)
	if !ok {
		return errorResult("artifact_id is required"), nil
	}
	artifactID := int64(artifactIDFloat)

	filePattern := ""
	if fp, ok := args["file_pattern"].(string); ok {
		filePattern = fp
	}

	maxFileSize := int64(1024 * 1024) // 1MB default
	if mfs, ok := args["max_file_size"].(float64); ok && mfs > 0 {
		maxFileSize = int64(mfs)
	}

	s.log.Infof("Getting artifact %d (pattern: %s, max_size: %d)", artifactID, filePattern, maxFileSize)

	content, err := s.client.GetArtifactContent(ctx, artifactID, filePattern, maxFileSize)
	if err != nil {
		return errorResult(s.formatAuthError(err, fmt.Sprintf("failed to get artifact %d", artifactID))), nil
	}

	return jsonResultPretty(content)
}

func (s *MCPServer) downloadArtifact(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := request.GetArguments()

	artifactIDFloat, ok := args["artifact_id"].(float64)
	if !ok {
		return errorResult("artifact_id is required"), nil
	}
	artifactID := int64(artifactIDFloat)

	outputPath := ""
	if op, ok := args["output_path"].(string); ok {
		outputPath = op
	}

	s.log.Infof("Downloading artifact %d to %s", artifactID, outputPath)

	result, err := s.client.DownloadArtifact(ctx, artifactID, outputPath)
	if err != nil {
		return errorResult(s.formatAuthError(err, fmt.Sprintf("failed to download artifact %d", artifactID))), nil
	}

	return jsonResultPretty(result)
}

// getFormat returns the format from config or default
func (s *MCPServer) getFormat() string {
	if s.config.DefaultFormat != "" {
		return s.config.DefaultFormat
	}
	return "compact"
}

func (s *MCPServer) GetServer() *server.MCPServer {
	return s.srv
}
