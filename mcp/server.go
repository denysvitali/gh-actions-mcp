package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

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

// jsonResult returns a successful JSON response
func jsonResult(data interface{}) (*mcp.CallToolResult, error) {
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

	ghClient := github.NewClient(cfg.Token, cfg.RepoOwner, cfg.RepoName)

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
	), s.listWorkflows)

	// Tool: get_workflow_runs
	s.srv.AddTool(mcp.NewTool("get_workflow_runs",
		mcp.WithDescription("Get recent runs for a specific workflow"),
		mcp.WithString("workflow_id",
			mcp.Description("The workflow ID or name (e.g., '12345678' or 'CI')"),
			mcp.Required(),
		),
		mcp.WithNumber("limit",
			mcp.Description("Maximum number of runs to return (default: 10)"),
			mcp.DefaultNumber(10),
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

	return jsonResult(status)
}

func (s *MCPServer) listWorkflows(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	limit := s.getLimit()

	if l, ok := request.GetArguments()["limit"]; ok {
		if n, err := strconv.Atoi(fmt.Sprintf("%.0f", l)); err == nil {
			limit = n
		}
	}

	s.log.Infof("Listing workflows for %s/%s (limit: %d)", s.config.RepoOwner, s.config.RepoName, limit)

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

	// Try to parse as ID first
	var workflowIDInt int64
	var runs []*github.WorkflowRun

	if id, err := strconv.ParseInt(workflowID, 10, 64); err == nil {
		runs, err = s.client.GetWorkflowRuns(ctx, id)
		if err != nil {
			return errorResult(s.formatAuthError(err, "failed to get workflow runs")), nil
		}
	} else {
		// Try by name - list workflows and find by name
		workflows, err := s.client.GetWorkflows(ctx)
		if err != nil {
			return errorResult(s.formatAuthError(err, "failed to get workflows")), nil
		}

		for _, w := range workflows {
			if w.Name == workflowID {
				workflowIDInt = w.ID
				break
			}
		}

		if workflowIDInt == 0 {
			return errorResult(fmt.Sprintf("workflow %s not found", workflowID)), nil
		}

		runs, err = s.client.GetWorkflowRuns(ctx, workflowIDInt)
		if err != nil {
			return errorResult(fmt.Sprintf("failed to get workflow runs: %v", err)), nil
		}
	}

	// Convert to our type with limit
	result := make([]*github.WorkflowRun, 0, limit)
	for _, run := range runs {
		if len(result) >= limit {
			break
		}
		result = append(result, run)
	}

	return jsonResult(result)
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

	return textResult(fmt.Sprintf("Successfully triggered workflow %s on branch %s", workflowID, ref)), nil
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

	output := fmt.Sprintf("Workflow run %d completed:\n"+
		"  Name: %s\n"+
		"  Status: %s\n"+
		"  Conclusion: %s\n"+
		"  Branch: %s\n"+
		"  Event: %s\n"+
		"  Actor: %s\n"+
		"  URL: %s\n"+
		"  Elapsed: %v\n"+
		"  Polls: %d",
		run.ID, run.Name, status, run.Conclusion, run.Branch, run.Event,
		run.Actor, run.URL, result.Elapsed, result.PollCount)

	return textResult(output), nil
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

	s.log.Infof("Getting workflow logs for run %d on %s/%s (head: %d, tail: %d, filter: %q, filter_regex: %q, context: %d)",
		runID, s.config.RepoOwner, s.config.RepoName, head, tail, filter, filterRegex, contextLines)

	// Create filter options
	var filterOpts *github.LogFilterOptions
	if filter != "" || filterRegex != "" {
		filterOpts = &github.LogFilterOptions{
			Filter:       filter,
			FilterRegex:  filterRegex,
			ContextLines: contextLines,
		}
	}

	logs, err := s.client.GetWorkflowLogs(ctx, runID, head, tail, filterOpts)
	if err != nil {
		return errorResult(s.formatAuthError(err, "failed to get workflow logs")), nil
	}

	return textResult(logs), nil
}

func (s *MCPServer) GetServer() *server.MCPServer {
	return s.srv
}
