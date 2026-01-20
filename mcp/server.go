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
	DefaultListLimit = 10  // Default max items for lists
	DefaultLogLines  = 100 // Default max lines for logs
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
	// Tool: get_actions_status
	s.srv.AddTool(mcp.NewTool("get_actions_status",
		mcp.WithDescription("Get the current status of GitHub Actions for the repository, including recent workflow runs and statistics"),
		mcp.WithNumber("limit",
			mcp.Description("Maximum number of recent runs to return (default: 10)"),
			mcp.DefaultNumber(10),
		),
	), s.getActionsStatus)

	// Tool: list_workflows
	s.srv.AddTool(mcp.NewTool("list_workflows",
		mcp.WithDescription("List all workflows available in the repository"),
		mcp.WithNumber("limit",
			mcp.Description("Maximum number of workflows to return (default: 10)"),
			mcp.DefaultNumber(10),
		),
		mcp.WithString("format",
			mcp.Description("Output format: compact (default, single-line JSON) or pretty (indented JSON)"),
			mcp.DefaultString("compact"),
		),
	), s.listWorkflows)

	// Tool: get_workflow_runs
	s.srv.AddTool(mcp.NewTool("get_workflow_runs",
		mcp.WithDescription("Get recent runs for a specific workflow"),
		mcp.WithString("workflow_id",
			mcp.Description("The workflow ID or name (e.g., '12345678' or 'CI')"),
			mcp.Required(),
		),
	), s.getWorkflowRuns)

	// Tool: trigger_workflow
	s.srv.AddTool(mcp.NewTool("trigger_workflow",
		mcp.WithDescription("Trigger a workflow to run manually"),
		mcp.WithString("workflow_id",
			mcp.Description("The workflow ID or name (e.g., '12345678' or 'CI')"),
			mcp.Required(),
		),
		mcp.WithString("ref",
			mcp.Description("The branch or tag to run the workflow on (default: main)"),
			mcp.DefaultString("main"),
		),
	), s.triggerWorkflow)

	// Tool: cancel_workflow_run
	s.srv.AddTool(mcp.NewTool("cancel_workflow_run",
		mcp.WithDescription("Cancel a running workflow"),
		mcp.WithNumber("run_id",
			mcp.Description("The workflow run ID to cancel"),
			mcp.Required(),
		),
	), s.cancelWorkflowRun)

	// Tool: rerun_workflow
	s.srv.AddTool(mcp.NewTool("rerun_workflow",
		mcp.WithDescription("Rerun a failed workflow"),
		mcp.WithNumber("run_id",
			mcp.Description("The workflow run ID to rerun"),
			mcp.Required(),
		),
	), s.rerunWorkflow)

	// Tool: wait_workflow_run
	s.srv.AddTool(mcp.NewTool("wait_workflow_run",
		mcp.WithDescription("Wait for a workflow run to complete, polling continuously for status updates"),
		mcp.WithNumber("run_id",
			mcp.Description("The workflow run ID to wait for"),
			mcp.Required(),
		),
		mcp.WithNumber("poll_interval",
			mcp.Description("Polling interval in seconds (default: 5)"),
			mcp.DefaultNumber(5),
		),
		mcp.WithNumber("timeout",
			mcp.Description("Maximum time to wait in seconds (default: 600)"),
			mcp.DefaultNumber(600),
		),
	), s.waitWorkflowRun)

	// Tool: get_workflow_logs
	s.srv.AddTool(mcp.NewTool("get_workflow_logs",
		mcp.WithDescription("Get the logs for a specific workflow run, with optional filtering and line limiting"),
		mcp.WithNumber("run_id",
			mcp.Description("The workflow run ID to get logs for"),
			mcp.Required(),
		),
		mcp.WithNumber("tail",
			mcp.Description("Return the last N lines of logs (e.g., 100 for the last 100 lines)"),
		),
		mcp.WithNumber("head",
			mcp.Description("Return the first N lines of logs (e.g., 100 for the first 100 lines)"),
		),
		mcp.WithString("filter",
			mcp.Description("Filter logs to lines containing this substring (case-insensitive). Mutually exclusive with filter_regex."),
		),
		mcp.WithString("filter_regex",
			mcp.Description("Filter logs to lines matching this regular expression pattern. Mutually exclusive with filter."),
		),
		mcp.WithNumber("context",
			mcp.Description("Number of lines to show before and after each match (like grep -C). Only applies when filter or filter_regex is used. Default: 0"),
		),
		mcp.WithBoolean("no_headers",
			mcp.Description("Don't print file headers (=== filename ===)"),
		),
	), s.getWorkflowLogs)
}

func (s *MCPServer) getActionsStatus(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	limit := s.getLimit()

	if l, ok := request.GetArguments()["limit"]; ok {
		if n, err := strconv.Atoi(fmt.Sprintf("%.0f", l)); err == nil {
			limit = n
		}
	}

	s.log.Infof("Getting actions status for %s/%s (limit: %d)", s.config.RepoOwner, s.config.RepoName, limit)

	status, err := s.client.GetActionsStatus(ctx, limit)
	if err != nil {
		return errorResult(s.formatAuthError(err, "failed to get actions status")), nil
	}

	// Get commit and branch for context
	commit, _ := github.GetLastCommit()
	branch, _ := github.GetCurrentBranch()

	formattedOutput := formatActionsStatus(status, commit, branch)
	return textResult(formattedOutput), nil
}

func (s *MCPServer) listWorkflows(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	limit := s.getLimit()

	if l, ok := request.GetArguments()["limit"]; ok {
		if n, err := strconv.Atoi(fmt.Sprintf("%.0f", l)); err == nil {
			limit = n
		}
	}

	format := "compact"
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

	if format == "pretty" {
		return jsonResultPretty(result)
	}
	return jsonResult(result)
}

func (s *MCPServer) getWorkflowRuns(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	limit := s.getLimit()

	workflowID, ok := request.GetArguments()["workflow_id"].(string)
	if !ok || workflowID == "" {
		return errorResult("workflow_id is required"), nil
	}

	if l, ok := request.GetArguments()["limit"]; ok {
		if n, err := strconv.Atoi(fmt.Sprintf("%.0f", l)); err == nil {
			limit = n
		}
	}

	// Extract branch parameter or auto-detect
	branch := ""
	if b, ok := request.GetArguments()["branch"].(string); ok && b != "" {
		branch = b
	} else {
		// Try to auto-detect branch from git repository
		if detectedBranch, err := github.GetCurrentBranch(); err == nil {
			branch = detectedBranch
			s.log.Debugf("Auto-detected branch: %s", branch)
		} else {
			s.log.Debugf("Could not auto-detect branch: %v (continuing without branch filter)", err)
		}
	}

	// Resolve workflow ID and name using the shared helper
	workflowIDInt, workflowName, err := s.client.ResolveWorkflowID(ctx, workflowID)
	if err != nil {
		return errorResult(s.formatAuthError(err, "failed to resolve workflow ID")), nil
	}

	// Get workflow runs
	runs, err := s.client.GetWorkflowRuns(ctx, workflowIDInt, branch)
	if err != nil {
		return errorResult(s.formatAuthError(err, "failed to get workflow runs")), nil
	}

	// Convert to our type with limit
	result := make([]*github.WorkflowRun, 0, limit)
	for _, run := range runs {
		if len(result) >= limit {
			break
		}
		result = append(result, run)
	}

	// Get commit info for context
	commit, _ := github.GetLastCommit()

	formattedOutput := formatWorkflowRuns(result, workflowName, branch, commit)
	return textResult(formattedOutput), nil
}

func (s *MCPServer) triggerWorkflow(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	workflowID, ok := request.GetArguments()["workflow_id"].(string)
	if !ok || workflowID == "" {
		return errorResult("workflow_id is required"), nil
	}

	ref := "main"
	if r, ok := request.GetArguments()["ref"].(string); ok && r != "" {
		ref = r
	}

	s.log.Infof("Triggering workflow %s on %s/%s (ref: %s)", workflowID, s.config.RepoOwner, s.config.RepoName, ref)

	err := s.client.TriggerWorkflow(ctx, workflowID, ref)
	if err != nil {
		return errorResult(s.formatAuthError(err, "failed to trigger workflow")), nil
	}

	// Get commit info for context
	commit, _ := github.GetLastCommit()
	branch, _ := github.GetCurrentBranch()

	output := fmt.Sprintf("Workflow triggered: %s\n  Branch: %s\n  %s",
		workflowID, ref, formatCommitInfo(commit))

	if branch != "" && branch != ref {
		output += fmt.Sprintf("\n  Note: current branch is '%s'", branch)
	}

	return textResult(output), nil
}

func (s *MCPServer) cancelWorkflowRun(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	runID, ok := extractRunID(request.GetArguments())
	if !ok {
		return errorResult("run_id is required"), nil
	}

	s.log.Infof("Cancelling workflow run %d on %s/%s", runID, s.config.RepoOwner, s.config.RepoName)

	err := s.client.CancelWorkflowRun(ctx, runID)
	if err != nil {
		return errorResult(s.formatAuthError(err, "failed to cancel workflow run")), nil
	}

	return textResult(fmt.Sprintf("Successfully cancelled workflow run %d", runID)), nil
}

func (s *MCPServer) rerunWorkflow(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	runID, ok := extractRunID(request.GetArguments())
	if !ok {
		return errorResult("run_id is required"), nil
	}

	s.log.Infof("Rerunning workflow run %d on %s/%s", runID, s.config.RepoOwner, s.config.RepoName)

	err := s.client.RerunWorkflowRun(ctx, runID)
	if err != nil {
		return errorResult(s.formatAuthError(err, "failed to rerun workflow")), nil
	}

	return textResult(fmt.Sprintf("Successfully triggered rerun for workflow run %d", runID)), nil
}

func (s *MCPServer) waitWorkflowRun(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	runID, ok := extractRunID(request.GetArguments())
	if !ok {
		return errorResult("run_id is required"), nil
	}

	pollInterval := 5
	if p, ok := request.GetArguments()["poll_interval"].(float64); ok {
		pollInterval = int(p)
	}

	timeout := 600
	if t, ok := request.GetArguments()["timeout"].(float64); ok {
		timeout = int(t)
	}

	s.log.Infof("Waiting for workflow run %d on %s/%s (poll_interval: %ds, timeout: %ds)",
		runID, s.config.RepoOwner, s.config.RepoName, pollInterval, timeout)

	result, err := s.client.WaitForWorkflowRun(ctx, runID, pollInterval, timeout)
	if err != nil {
		return errorResult(s.formatAuthError(err, "failed to wait for workflow")), nil
	}

	run := result.Run
	status := "completed"
	if result.TimedOut {
		status = "timed_out"
	}

	output := map[string]interface{}{
		"id":         run.ID,
		"name":       run.Name,
		"status":     status,
		"conclusion": run.Conclusion,
		"branch":     run.Branch,
		"event":      run.Event,
		"actor":      run.Actor,
		"url":        run.URL,
		"elapsed":    result.Elapsed.String(),
		"polls":      result.PollCount,
	}
	return jsonResult(output)
}

func (s *MCPServer) getWorkflowLogs(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	runID, ok := extractRunID(request.GetArguments())
	if !ok {
		return errorResult("run_id is required"), nil
	}

	head := 0
	if h, ok := request.GetArguments()["head"].(float64); ok && h > 0 {
		head = int(h)
	}

	tail := 0
	if t, ok := request.GetArguments()["tail"].(float64); ok && t > 0 {
		tail = int(t)
	} else if head == 0 {
		// Apply default log limit when neither head nor tail is specified
		tail = s.getLogLimit()
	}

	// Extract filter parameters
	filter := ""
	if f, ok := request.GetArguments()["filter"].(string); ok {
		filter = f
	}

	filterRegex := ""
	if fr, ok := request.GetArguments()["filter_regex"].(string); ok {
		filterRegex = fr
	}

	// Validate mutual exclusivity
	if filter != "" && filterRegex != "" {
		return errorResult("filter and filter_regex are mutually exclusive; use only one"), nil
	}

	contextLines := 0
	if c, ok := request.GetArguments()["context"].(float64); ok && c > 0 {
		contextLines = int(c)
	}

	noHeaders := false
	if nh, ok := request.GetArguments()["no_headers"].(bool); ok && nh {
		noHeaders = true
	}

	s.log.Infof("Getting workflow logs for run %d on %s/%s (head: %d, tail: %d, filter: %q, filter_regex: %q, context: %d, no_headers: %v)",
		runID, s.config.RepoOwner, s.config.RepoName, head, tail, filter, filterRegex, contextLines, noHeaders)

	// Create filter options
	var filterOpts *github.LogFilterOptions
	if filter != "" || filterRegex != "" {
		filterOpts = &github.LogFilterOptions{
			Filter:       filter,
			FilterRegex:  filterRegex,
			ContextLines: contextLines,
		}
	}

	logs, err := s.client.GetWorkflowLogs(ctx, runID, head, tail, noHeaders, filterOpts)
	if err != nil {
		return errorResult(s.formatAuthError(err, "failed to get workflow logs")), nil
	}

	return textResult(logs), nil
}

func (s *MCPServer) GetServer() *server.MCPServer {
	return s.srv
}
