package mcp

import (
	"context"
	"encoding/json"
	"fmt"
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

// formatCommitInfo formats commit information with clear section header
func formatCommitInfo(commit *github.CommitInfo) string {
	if commit == nil {
		return "Commit: (not available)"
	}
	return fmt.Sprintf("Commit: %s\n  Author: %s\n  Date:   %s\n  %s",
		commit.SHA, commit.Author, commit.Date, commit.Msg)
}

// formatWorkflowRun formats a single workflow run with clear field labels
func formatWorkflowRun(run *github.WorkflowRun) string {
	icon := "○"
	switch run.Conclusion {
	case "success":
		icon = "✓"
	case "failure":
		icon = "✗"
	case "cancelled":
		icon = "⊘"
	}
	return fmt.Sprintf("%s Run #%d | %s | %s | %s | %s\n    ID: %d | %s",
		icon, run.RunNumber, run.Status, run.Conclusion, run.Branch, run.Event, run.ID, run.URL)
}

// formatWorkflowRunDetail formats a workflow run with full details
func formatWorkflowRunDetail(run *github.WorkflowRun) string {
	return fmt.Sprintf("Run #%d\n  ID:         %d\n  Status:     %s\n  Conclusion: %s\n  Branch:     %s\n  Event:      %s\n  Actor:      %s\n  Created:    %s\n  URL:        %s",
		run.RunNumber, run.ID, run.Status, run.Conclusion, run.Branch, run.Event, run.Actor, run.CreatedAt, run.URL)
}

// formatActionsStatus formats the actions status with clear section headers
func formatActionsStatus(status *github.ActionsStatus, commit *github.CommitInfo, branch string) string {
	var sb strings.Builder

	// Header with repo info
	sb.WriteString("GitHub Actions Status\n")
	sb.WriteString(strings.Repeat("=", 40))
	sb.WriteString("\n\n")

	// Commit context (if available)
	if commit != nil {
		sb.WriteString("Last Commit\n")
		sb.WriteString(strings.Repeat("-", 20))
		sb.WriteString("\n")
		sb.WriteString(formatCommitInfo(commit))
		if branch != "" {
			sb.WriteString(fmt.Sprintf("\n  Branch: %s", branch))
		}
		sb.WriteString("\n\n")
	}

	// Statistics summary
	sb.WriteString("Statistics\n")
	sb.WriteString(strings.Repeat("-", 20))
	sb.WriteString("\n")
	sb.WriteString(fmt.Sprintf("  Total workflows:  %d\n", status.TotalWorkflows))
	sb.WriteString(fmt.Sprintf("  Total runs:       %d\n", status.TotalRuns))
	sb.WriteString(fmt.Sprintf("  Success:          %d\n", status.SuccessfulRuns))
	sb.WriteString(fmt.Sprintf("  Failed:           %d\n", status.FailedRuns))
	sb.WriteString(fmt.Sprintf("  In progress:      %d\n", status.InProgressRuns))
	sb.WriteString(fmt.Sprintf("  Queued:           %d\n", status.QueuedRuns))
	sb.WriteString("\n")

	// Recent runs
	if len(status.RecentRuns) > 0 {
		sb.WriteString("Recent Workflow Runs\n")
		sb.WriteString(strings.Repeat("-", 20))
		sb.WriteString("\n")
		for _, run := range status.RecentRuns {
			sb.WriteString(formatWorkflowRun(run))
			sb.WriteString("\n")
		}
	}

	return sb.String()
}

// formatWorkflowRuns formats workflow runs with clear section headers
func formatWorkflowRuns(runs []*github.WorkflowRun, workflowName, branch string, commit *github.CommitInfo) string {
	var sb strings.Builder

	sb.WriteString("Workflow Runs")
	sb.WriteString("\n")
	sb.WriteString(strings.Repeat("=", 40))
	sb.WriteString("\n\n")

	// Context header
	sb.WriteString("Context\n")
	sb.WriteString(strings.Repeat("-", 20))
	sb.WriteString("\n")
	sb.WriteString(fmt.Sprintf("  Workflow: %s\n", workflowName))
	if branch != "" {
		sb.WriteString(fmt.Sprintf("  Branch:   %s\n", branch))
	}
	if commit != nil {
		sb.WriteString(fmt.Sprintf("  Commit:   %s - %s\n", commit.SHA, commit.Msg))
	}
	sb.WriteString("\n")

	// Runs section
	sb.WriteString(fmt.Sprintf("Runs (%d total)\n", len(runs)))
	sb.WriteString(strings.Repeat("-", 20))
	sb.WriteString("\n")

	if len(runs) == 0 {
		sb.WriteString("  No runs found\n")
	} else {
		for _, run := range runs {
			sb.WriteString(formatWorkflowRun(run))
			sb.WriteString("\n")
		}
	}

	return sb.String()
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
		mcp.WithDescription("Get detailed information about a workflow run, including jobs, logs, or artifacts"),
		mcp.WithNumber("run_id",
			mcp.Description("The workflow run ID"),
			mcp.Required(),
		),
		mcp.WithString("element",
			mcp.Description("Element to retrieve: info (default), jobs, logs, or artifacts"),
			mcp.DefaultString("info"),
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
			mcp.Description("For element=logs: return the last N lines of logs (default: 50)"),
			mcp.DefaultNumber(50),
		),
		mcp.WithString("filter",
			mcp.Description("For element=logs: filter logs to lines containing this substring (case-insensitive)"),
		),
		mcp.WithString("filter_regex",
			mcp.Description("For element=logs: filter logs to lines matching this regex pattern"),
		),
		mcp.WithNumber("context",
			mcp.Description("For element=logs: number of lines to show before and after each match (default: 0)"),
			mcp.DefaultNumber(0),
		),
		mcp.WithBoolean("no_headers",
			mcp.Description("For element=logs: don't print file headers (=== filename ===)"),
		),
		mcp.WithString("format",
			mcp.Description("For element=info, jobs, artifacts: output format (compact/full, default: compact)"),
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
	case "artifacts":
		return s.getRunArtifacts(ctx, runID, args)
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

	tail := s.getLogLimit()
	if t, ok := args["tail"].(float64); ok && t > 0 {
		tail = int(t)
	}

	filter := ""
	if f, ok := args["filter"].(string); ok {
		filter = f
	}

	filterRegex := ""
	if fr, ok := args["filter_regex"].(string); ok {
		filterRegex = fr
	}

	if filter != "" && filterRegex != "" {
		return errorResult("filter and filter_regex are mutually exclusive"), nil
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
		Filter:       filter,
		FilterRegex:  filterRegex,
		ContextLines: contextLines,
	}

	logs, err := s.client.GetWorkflowLogs(ctx, runID, head, tail, noHeaders, filterOpts)
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

	tail := s.getLogLimit()
	if t, ok := args["tail"].(float64); ok && t > 0 {
		tail = int(t)
	}

	filter := ""
	if f, ok := args["filter"].(string); ok {
		filter = f
	}

	filterRegex := ""
	if fr, ok := args["filter_regex"].(string); ok {
		filterRegex = fr
	}

	if filter != "" && filterRegex != "" {
		return errorResult("filter and filter_regex are mutually exclusive"), nil
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
		Filter:       filter,
		FilterRegex:  filterRegex,
		ContextLines: contextLines,
	}

	logs, err := s.client.GetWorkflowJobLogs(ctx, jobID, head, tail, noHeaders, filterOpts)
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

func (s *MCPServer) getCheckStatus(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := request.GetArguments()

	ref := ""
	if r, ok := args["ref"].(string); ok && r != "" {
		ref = r
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
		ref = r
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
