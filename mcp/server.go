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
}

func (s *MCPServer) getActionsStatus(arguments map[string]interface{}) (*mcp.CallToolResult, error) {
	ctx := context.Background()
	limit := 10

	if l, ok := arguments["limit"]; ok {
		if n, err := strconv.Atoi(fmt.Sprintf("%.0f", l)); err == nil {
			limit = n
		}
	}

	s.log.Infof("Getting actions status for %s/%s (limit: %d)", s.config.RepoOwner, s.config.RepoName, limit)

	status, err := s.client.GetActionsStatus(ctx, limit)
	if err != nil {
		errMsg := fmt.Sprintf("failed to get actions status: %v", err)
		if config.IsAuthenticationError(err) {
			errMsg = fmt.Sprintf("authentication failed: %v\nMake sure GITHUB_TOKEN is set and has access to %s/%s", err, s.config.RepoOwner, s.config.RepoName)
		}
		return mcp.NewToolResultError(errMsg), nil
	}

	data, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to marshal status: %v", err)), nil
	}

	return mcp.NewToolResultText(string(data)), nil
}

func (s *MCPServer) listWorkflows(arguments map[string]interface{}) (*mcp.CallToolResult, error) {
	ctx := context.Background()

	s.log.Infof("Listing workflows for %s/%s", s.config.RepoOwner, s.config.RepoName)

	workflows, err := s.client.GetWorkflows(ctx)
	if err != nil {
		errMsg := fmt.Sprintf("failed to list workflows: %v", err)
		if config.IsAuthenticationError(err) {
			errMsg = fmt.Sprintf("authentication failed: %v\nMake sure GITHUB_TOKEN is set and has access to %s/%s", err, s.config.RepoOwner, s.config.RepoName)
		}
		return mcp.NewToolResultError(errMsg), nil
	}

	data, err := json.MarshalIndent(workflows, "", "  ")
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to marshal workflows: %v", err)), nil
	}

	return mcp.NewToolResultText(string(data)), nil
}

func (s *MCPServer) getWorkflowRuns(arguments map[string]interface{}) (*mcp.CallToolResult, error) {
	ctx := context.Background()
	limit := 10

	workflowID, ok := arguments["workflow_id"].(string)
	if !ok || workflowID == "" {
		return mcp.NewToolResultError("workflow_id is required"), nil
	}

	if l, ok := arguments["limit"]; ok {
		if n, err := strconv.Atoi(fmt.Sprintf("%.0f", l)); err == nil {
			limit = n
		}
	}

	// Try to parse as ID first
	var workflowIDInt int64
	var runs []*github.WorkflowRun
	var err error

	if id, err := strconv.ParseInt(workflowID, 10, 64); err == nil {
		runs, err = s.client.GetWorkflowRuns(ctx, id)
		if err != nil {
			errMsg := fmt.Sprintf("failed to get workflow runs: %v", err)
			if config.IsAuthenticationError(err) {
				errMsg = fmt.Sprintf("authentication failed: %v\nMake sure GITHUB_TOKEN is set and has access to %s/%s", err, s.config.RepoOwner, s.config.RepoName)
			}
			return mcp.NewToolResultError(errMsg), nil
		}
	} else {
		// Try by name - list workflows and find by name
		workflows, err := s.client.GetWorkflows(ctx)
		if err != nil {
			errMsg := fmt.Sprintf("failed to get workflows: %v", err)
			if config.IsAuthenticationError(err) {
				errMsg = fmt.Sprintf("authentication failed: %v\nMake sure GITHUB_TOKEN is set and has access to %s/%s", err, s.config.RepoOwner, s.config.RepoName)
			}
			return mcp.NewToolResultError(errMsg), nil
		}

		for _, w := range workflows {
			if w.Name == workflowID {
				workflowIDInt = w.ID
				break
			}
		}

		if workflowIDInt == 0 {
			return mcp.NewToolResultError(fmt.Sprintf("workflow %s not found", workflowID)), nil
		}

		runs, err = s.client.GetWorkflowRuns(ctx, workflowIDInt)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to get workflow runs: %v", err)), nil
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

	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to marshal runs: %v", err)), nil
	}

	return mcp.NewToolResultText(string(data)), nil
}

func (s *MCPServer) triggerWorkflow(arguments map[string]interface{}) (*mcp.CallToolResult, error) {
	ctx := context.Background()

	workflowID, ok := arguments["workflow_id"].(string)
	if !ok || workflowID == "" {
		return mcp.NewToolResultError("workflow_id is required"), nil
	}

	ref := "main"
	if r, ok := arguments["ref"].(string); ok && r != "" {
		ref = r
	}

	s.log.Infof("Triggering workflow %s on %s/%s (ref: %s)", workflowID, s.config.RepoOwner, s.config.RepoName, ref)

	err := s.client.TriggerWorkflow(ctx, workflowID, ref)
	if err != nil {
		errMsg := fmt.Sprintf("failed to trigger workflow: %v", err)
		if config.IsAuthenticationError(err) {
			errMsg = fmt.Sprintf("authentication failed: %v\nMake sure GITHUB_TOKEN is set and has access to %s/%s", err, s.config.RepoOwner, s.config.RepoName)
		}
		return mcp.NewToolResultError(errMsg), nil
	}

	return mcp.NewToolResultText(fmt.Sprintf("Successfully triggered workflow %s on branch %s", workflowID, ref)), nil
}

func (s *MCPServer) cancelWorkflowRun(arguments map[string]interface{}) (*mcp.CallToolResult, error) {
	ctx := context.Background()

	runIDFloat, ok := arguments["run_id"].(float64)
	if !ok {
		return mcp.NewToolResultError("run_id is required"), nil
	}
	runID := int64(runIDFloat)

	s.log.Infof("Cancelling workflow run %d on %s/%s", runID, s.config.RepoOwner, s.config.RepoName)

	err := s.client.CancelWorkflowRun(ctx, runID)
	if err != nil {
		errMsg := fmt.Sprintf("failed to cancel workflow run: %v", err)
		if config.IsAuthenticationError(err) {
			errMsg = fmt.Sprintf("authentication failed: %v\nMake sure GITHUB_TOKEN is set and has access to %s/%s", err, s.config.RepoOwner, s.config.RepoName)
		}
		return mcp.NewToolResultError(errMsg), nil
	}

	return mcp.NewToolResultText(fmt.Sprintf("Successfully cancelled workflow run %d", runID)), nil
}

func (s *MCPServer) rerunWorkflow(arguments map[string]interface{}) (*mcp.CallToolResult, error) {
	ctx := context.Background()

	runIDFloat, ok := arguments["run_id"].(float64)
	if !ok {
		return mcp.NewToolResultError("run_id is required"), nil
	}
	runID := int64(runIDFloat)

	s.log.Infof("Rerunning workflow run %d on %s/%s", runID, s.config.RepoOwner, s.config.RepoName)

	err := s.client.RerunWorkflowRun(ctx, runID)
	if err != nil {
		errMsg := fmt.Sprintf("failed to rerun workflow: %v", err)
		if config.IsAuthenticationError(err) {
			errMsg = fmt.Sprintf("authentication failed: %v\nMake sure GITHUB_TOKEN is set and has access to %s/%s", err, s.config.RepoOwner, s.config.RepoName)
		}
		return mcp.NewToolResultError(errMsg), nil
	}

	return mcp.NewToolResultText(fmt.Sprintf("Successfully triggered rerun for workflow run %d", runID)), nil
}

func (s *MCPServer) GetServer() *server.MCPServer {
	return s.srv
}
