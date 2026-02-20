package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/denysvitali/gh-actions-mcp/config"
	"github.com/denysvitali/gh-actions-mcp/github"
	ghapi "github.com/google/go-github/v69/github"

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

func (s *MCPServer) repoFromArgs(args map[string]interface{}) (string, string, error) {
	owner := s.config.RepoOwner
	repo := s.config.RepoName

	if v, ok := args["owner"].(string); ok && strings.TrimSpace(v) != "" {
		owner = strings.TrimSpace(v)
	}
	if v, ok := args["repo"].(string); ok && strings.TrimSpace(v) != "" {
		repo = strings.TrimSpace(v)
	}
	if v, ok := args["repo_owner"].(string); ok && strings.TrimSpace(v) != "" {
		owner = strings.TrimSpace(v)
	}
	if v, ok := args["repo_name"].(string); ok && strings.TrimSpace(v) != "" {
		repo = strings.TrimSpace(v)
	}

	if owner == "" || repo == "" {
		return "", "", fmt.Errorf("repository owner/repo not set. Provide owner and repo arguments")
	}
	return owner, repo, nil
}

func (s *MCPServer) clientFromArgs(args map[string]interface{}) (*github.Client, string, string, error) {
	owner, repo, err := s.repoFromArgs(args)
	if err != nil {
		return nil, "", "", err
	}
	perPageLimit := s.config.PerPageLimit
	if perPageLimit <= 0 {
		perPageLimit = 50
	}
	return github.NewClientWithPerPage(s.config.Token, owner, repo, perPageLimit), owner, repo, nil
}

// Helper functions to reduce repetition

// getLimit returns the limit from config or default
func (s *MCPServer) getLimit() int {
	if s.config.DefaultLimit > 0 {
		return s.config.DefaultLimit
	}
	return DefaultListLimit
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
		mcp.WithString("owner",
			mcp.Description("Optional: override repository owner for this call"),
		),
		mcp.WithString("repo",
			mcp.Description("Optional: override repository name for this call"),
		),
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
		mcp.WithDescription("Get workflow run details. Start with element=info, then use jobs/logs/log_sections/artifacts as needed."),
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
		mcp.WithDescription("Get workflow status summary for a commit/branch/tag (derived from workflow runs; no Checks API permission required)."),
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
		),
		mcp.WithString("format",
			mcp.Description("Output format: summary (default), compact, or full"),
			mcp.DefaultString("summary"),
		),
	), s.getCheckStatus)

	// Tool: wait_for_run
	s.srv.AddTool(mcp.NewTool("wait_for_run",
		mcp.WithDescription("Wait silently for a workflow run to complete (no output during polling)"),
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
		),
	), s.waitForRun)

	// Tool: wait_for_commit_checks
	s.srv.AddTool(mcp.NewTool("wait_for_commit_checks",
		mcp.WithDescription("Wait for all CI check runs for a commit ref (SHA, branch, or tag) to complete."),
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
		),
	), s.waitForCommitChecks)

	// Tool: manage_run
	s.srv.AddTool(mcp.NewTool("manage_run",
		mcp.WithDescription("Manage a workflow run (cancel, rerun, or rerun failed jobs)"),
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
		),
	), s.manageRun)

	// Tool: get_artifact
	s.srv.AddTool(mcp.NewTool("get_artifact",
		mcp.WithDescription("Get the contents of a workflow run artifact (stream without downloading to disk)"),
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
	), s.getArtifact)

	// Tool: download_artifact
	s.srv.AddTool(mcp.NewTool("download_artifact",
		mcp.WithDescription("Download a workflow run artifact to disk"),
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
			mcp.Description("Optional: path where to save the artifact (default: {artifact-name}.zip)"),
		),
	), s.downloadArtifact)
}

func (s *MCPServer) listWorkflows(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := request.GetArguments()
	limit := s.getLimit()

	if l, ok := args["limit"]; ok {
		if n, err := strconv.Atoi(fmt.Sprintf("%.0f", l)); err == nil {
			limit = n
		}
	}

	format := s.getFormat()
	if f, ok := args["format"].(string); ok {
		format = f
	}

	client, owner, repo, err := s.clientFromArgs(args)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	s.log.Infof("Listing workflows for %s/%s (limit: %d, format: %s)", owner, repo, limit, format)

	workflows, err := client.GetWorkflows(ctx)
	if err != nil {
		return errorResult(s.formatAuthErrorForRepo(err, "failed to list workflows", owner, repo)), nil
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
	client, owner, repo, err := s.clientFromArgs(args)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	// Build options
	opts := &github.ListRunsOptions{
		Per_page: s.getLimit(),
	}

	if perPage, ok := args["per_page"].(float64); ok && perPage > 0 {
		opts.Per_page = int(perPage)
		if opts.Per_page > 100 {
			opts.Per_page = 100
		}
	}

	if workflowIDNum, ok := args["workflow_id"].(float64); ok && workflowIDNum > 0 {
		workflowIDStr := fmt.Sprintf("%.0f", workflowIDNum)
		if workflowID, _, err := client.ResolveWorkflowID(ctx, workflowIDStr); err == nil {
			opts.WorkflowID = &workflowID
		}
	}

	if branch, ok := args["branch"].(string); ok && branch != "" {
		opts.Branch = branch
	} else if owner == s.config.RepoOwner && repo == s.config.RepoName {
		// Auto-detect branch only for the configured repo (not cross-repo overrides)
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

	s.log.Infof("Listing runs for %s/%s", owner, repo)

	runs, err := client.ListRepositoryWorkflowRunsWithOptions(ctx, opts)
	if err != nil {
		return errorResult(s.formatAuthErrorForRepo(err, "failed to list workflow runs", owner, repo)), nil
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
				ID:         r.ID,
				Name:       r.Name,
				Status:     r.Status,
				Conclusion: r.Conclusion,
				Branch:     r.Branch,
				Event:      r.Event,
				Actor:      r.Actor,
				CreatedAt:  r.CreatedAt,
				UpdatedAt:  r.UpdatedAt,
				URL:        r.URL,
				RunNumber:  r.RunNumber,
				WorkflowID: r.WorkflowID,
				HeadSHA:    r.HeadSHA,
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
				SHA:    r.HeadSHA,
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
	client, owner, repo, err := s.clientFromArgs(args)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	runID, ok := extractRunID(args)
	if !ok {
		return errorResult("run_id is required"), nil
	}

	element := "info"
	if e, ok := args["element"].(string); ok {
		element = strings.TrimSpace(strings.ToLower(e))
	}

	if !isValidRunElement(element) {
		return errorResult(fmt.Sprintf("invalid element %q. Allowed values: %s", element, strings.Join(validRunElements, ", "))), nil
	}

	s.log.Infof("Getting run %d (element: %s)", runID, element)

	switch element {
	case "jobs":
		return s.getRunJobs(ctx, client, owner, repo, runID, args)
	case "logs":
		return s.getRunLogs(ctx, client, owner, repo, runID, args)
	case "log_files":
		return s.getLogFiles(ctx, client, owner, repo, runID, args)
	case "log_sections":
		return s.getLogSections(ctx, client, owner, repo, runID, args)
	case "artifacts":
		return s.getRunArtifacts(ctx, client, owner, repo, runID, args)
	case "artifact_content":
		return s.getArtifactContent(ctx, client, owner, repo, args)
	default: // info
		return s.getRunInfo(ctx, client, owner, repo, runID, args)
	}
}

func (s *MCPServer) getRunInfo(ctx context.Context, client *github.Client, owner, repo string, runID int64, args map[string]interface{}) (*mcp.CallToolResult, error) {
	run, err := client.GetWorkflowRun(ctx, runID)
	if err != nil {
		return errorResult(s.formatAuthErrorForRepo(err, fmt.Sprintf("Run ID %d not found", runID), owner, repo)), nil
	}

	format := s.getFormat()
	if f, ok := args["format"].(string); ok {
		format = f
	}

	switch format {
	case "full":
		result := &github.WorkflowRunFull{
			ID:         run.ID,
			Name:       run.Name,
			Status:     run.Status,
			Conclusion: run.Conclusion,
			Branch:     run.Branch,
			Event:      run.Event,
			Actor:      run.Actor,
			CreatedAt:  run.CreatedAt,
			UpdatedAt:  run.UpdatedAt,
			URL:        run.URL,
			RunNumber:  run.RunNumber,
			WorkflowID: run.WorkflowID,
			HeadSHA:    run.HeadSHA,
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
			SHA:    run.HeadSHA,
			Event:  run.Event,
			Actor:  run.Actor,
			URL:    run.URL,
		}
		return jsonResult(result)
	}
}

func (s *MCPServer) getRunJobs(ctx context.Context, client *github.Client, owner, repo string, runID int64, args map[string]interface{}) (*mcp.CallToolResult, error) {
	filter := ""
	if f, ok := args["filter"].(string); ok {
		filter = f
	}

	attemptNumber := 0
	if an, ok := args["attempt_number"].(float64); ok && an > 0 {
		attemptNumber = int(an)
	}

	jobs, err := client.GetWorkflowJobs(ctx, runID, filter, attemptNumber)
	if err != nil {
		return errorResult(s.formatAuthErrorForRepo(err, fmt.Sprintf("failed to get jobs for run %d", runID), owner, repo)), nil
	}

	format := s.getFormat()
	if f, ok := args["format"].(string); ok {
		format = f
	}

	if format == "full" {
		return jsonResultPretty(jobs)
	}
	return jsonResult(jobs)
}

func (s *MCPServer) getRunLogs(ctx context.Context, client *github.Client, owner, repo string, runID int64, args map[string]interface{}) (*mcp.CallToolResult, error) {
	// Check if getting logs for a specific job
	if jobIDFloat, ok := args["job_id"].(float64); ok {
		jobID := int64(jobIDFloat)
		return s.getJobLogs(ctx, client, owner, repo, jobID, args)
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
		logs, err = client.GetLogSection(ctx, runID, 0, section, filterOpts)
	} else {
		// Get all logs with optional filtering
		logs, err = client.GetWorkflowLogsWithPattern(ctx, runID, head, tail, offset, noHeaders, filePattern, filterOpts)
	}

	if err != nil {
		return errorResult(s.formatAuthErrorForRepo(err, fmt.Sprintf("failed to get logs for run %d", runID), owner, repo)), nil
	}

	return textResult(logs), nil
}

func (s *MCPServer) getJobLogs(ctx context.Context, client *github.Client, owner, repo string, jobID int64, args map[string]interface{}) (*mcp.CallToolResult, error) {
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
		logs, err = client.GetLogSection(ctx, 0, jobID, section, filterOpts)
	} else {
		// Get all job logs with optional filtering
		logs, err = client.GetWorkflowJobLogs(ctx, jobID, head, tail, offset, noHeaders, filterOpts)
	}

	if err != nil {
		return errorResult(s.formatAuthErrorForRepo(err, fmt.Sprintf("failed to get logs for job %d", jobID), owner, repo)), nil
	}

	return textResult(logs), nil
}

func (s *MCPServer) getRunArtifacts(ctx context.Context, client *github.Client, owner, repo string, runID int64, args map[string]interface{}) (*mcp.CallToolResult, error) {
	artifacts, err := client.GetWorkflowRunArtifacts(ctx, runID)
	if err != nil {
		return errorResult(s.formatAuthErrorForRepo(err, fmt.Sprintf("failed to get artifacts for run %d", runID), owner, repo)), nil
	}

	format := s.getFormat()
	if f, ok := args["format"].(string); ok {
		format = f
	}

	if format == "full" {
		return jsonResultPretty(artifacts)
	}
	return jsonResult(artifacts)
}

func (s *MCPServer) getArtifactContent(ctx context.Context, client *github.Client, owner, repo string, args map[string]interface{}) (*mcp.CallToolResult, error) {
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

	content, err := client.GetArtifactContent(ctx, artifactID, filePattern, maxFileSize)
	if err != nil {
		return errorResult(s.formatAuthErrorForRepo(err, fmt.Sprintf("failed to get artifact content %d", artifactID), owner, repo)), nil
	}

	return jsonResultPretty(content)
}

func (s *MCPServer) getLogFiles(ctx context.Context, client *github.Client, owner, repo string, runID int64, args map[string]interface{}) (*mcp.CallToolResult, error) {
	logFiles, err := client.GetWorkflowLogFiles(ctx, runID)
	if err != nil {
		return errorResult(s.formatAuthErrorForRepo(err, fmt.Sprintf("failed to get log files for run %d", runID), owner, repo)), nil
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

func (s *MCPServer) getLogSections(ctx context.Context, client *github.Client, owner, repo string, runID int64, args map[string]interface{}) (*mcp.CallToolResult, error) {
	// Check if getting sections for a specific job
	var jobID int64
	if jobIDFloat, ok := args["job_id"].(float64); ok {
		jobID = int64(jobIDFloat)
	}

	s.log.Infof("Getting log sections for run %d (job_id: %d)", runID, jobID)

	sections, err := client.ListLogSections(ctx, runID, jobID)
	if err != nil {
		return errorResult(s.formatAuthErrorForRepo(err, fmt.Sprintf("failed to get log sections for run %d", runID), owner, repo)), nil
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
	client, owner, repo, err := s.clientFromArgs(args)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	ref := ""
	if r, ok := args["ref"].(string); ok && r != "" {
		ref = strings.TrimSpace(r)
	} else if owner == s.config.RepoOwner && repo == s.config.RepoName {
		// Auto-detect ref only for the configured repo (not cross-repo overrides)
		if commit, err := github.GetLastCommit(); err == nil {
			ref = commit.SHA
		} else {
			return errorResult("could not determine ref - please specify explicitly"), nil
		}
	} else {
		return errorResult("ref is required when querying a different repository"), nil
	}

	opts := &github.GetCheckRunsOptions{}

	if checkName, ok := args["check_name"].(string); ok && checkName != "" {
		opts.CheckName = checkName
	}

	if status, ok := args["status"].(string); ok && status != "" {
		opts.Status = status
	}

	filterMode := "latest"
	if filter, ok := args["filter"].(string); ok && filter != "" {
		filterMode = strings.TrimSpace(strings.ToLower(filter))
	}
	if filterMode != "latest" && filterMode != "all" {
		return errorResult(fmt.Sprintf("invalid filter %q. Allowed values: latest, all", filterMode)), nil
	}
	opts.Filter = filterMode

	format := "summary"
	if f, ok := args["format"].(string); ok && f != "" {
		format = strings.TrimSpace(strings.ToLower(f))
	}
	if format != "summary" && format != "compact" && format != "full" {
		return errorResult(fmt.Sprintf("invalid format %q. Allowed values: summary, compact, full", format)), nil
	}

	status, err := client.GetCheckRunsForRef(ctx, ref, opts)
	if err != nil {
		return errorResult(s.formatAuthErrorForRepo(err, "failed to get check status", owner, repo)), nil
	}

	switch format {
	case "full":
		return jsonResult(status)
	case "compact":
		return jsonResult(status)
	default: // summary
		return textResult(formatWorkflowStatusSummary(ref, status, filterMode)), nil
	}
}

func (s *MCPServer) waitForRun(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := request.GetArguments()
	client, owner, repo, err := s.clientFromArgs(args)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	runID, ok := extractRunID(args)
	if !ok {
		return errorResult("run_id is required"), nil
	}

	timeoutMinutes := 30
	if tm, ok := args["timeout_minutes"].(float64); ok && tm > 0 {
		timeoutMinutes = int(tm)
		if timeoutMinutes > 120 {
			timeoutMinutes = 120
		}
	}

	s.log.Infof("Waiting for run %d (timeout: %dm)", runID, timeoutMinutes)

	result, err := client.WaitForRun(ctx, runID, timeoutMinutes)
	if err != nil {
		if result == nil || !result.TimeoutReached {
			return errorResult(s.formatAuthErrorForRepo(err, "failed to wait for run", owner, repo)), nil
		}
	}

	return jsonResult(result)
}

func (s *MCPServer) waitForCommitChecks(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := request.GetArguments()
	client, owner, repo, err := s.clientFromArgs(args)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	ref := ""
	if r, ok := args["ref"].(string); ok && r != "" {
		ref = strings.TrimSpace(r)
	}

	timeoutMinutes := 30
	if tm, ok := args["timeout_minutes"].(float64); ok && tm > 0 {
		timeoutMinutes = int(tm)
		if timeoutMinutes > 120 {
			timeoutMinutes = 120
		}
	}

	s.log.Infof("Waiting for checks on ref %s (timeout: %dm)", ref, timeoutMinutes)

	result, err := client.WaitForCommitChecks(ctx, ref, timeoutMinutes)
	if err != nil {
		if result == nil || !result.TimeoutReached {
			return errorResult(s.formatAuthErrorForRepo(err, "failed to wait for checks", owner, repo)), nil
		}
	}

	return jsonResult(result)
}

func (s *MCPServer) manageRun(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := request.GetArguments()
	client, owner, repo, err := s.clientFromArgs(args)
	if err != nil {
		return errorResult(err.Error()), nil
	}

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

	s.log.Infof("Managing run %d on %s/%s: %s", runID, owner, repo, action)

	result, err := client.ManageRun(ctx, runID, action)
	if err != nil {
		return errorResult(s.formatAuthErrorForRepo(err, "failed to manage run", owner, repo)), nil
	}

	if result.Status == "success" {
		return textResult(result.Message), nil
	}
	return errorResult(result.Message), nil
}

func (s *MCPServer) getArtifact(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := request.GetArguments()
	client, owner, repo, err := s.clientFromArgs(args)
	if err != nil {
		return errorResult(err.Error()), nil
	}

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

	content, err := client.GetArtifactContent(ctx, artifactID, filePattern, maxFileSize)
	if err != nil {
		return errorResult(s.formatAuthErrorForRepo(err, fmt.Sprintf("failed to get artifact %d", artifactID), owner, repo)), nil
	}

	return jsonResultPretty(content)
}

func (s *MCPServer) downloadArtifact(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := request.GetArguments()
	client, owner, repo, err := s.clientFromArgs(args)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	artifactIDFloat, ok := args["artifact_id"].(float64)
	if !ok {
		return errorResult("artifact_id is required"), nil
	}
	artifactID := int64(artifactIDFloat)

	outputPath := ""
	if op, ok := args["output_path"].(string); ok {
		// Reject absolute paths and path components that escape the current directory
		if filepath.IsAbs(op) || strings.Contains(op, "..") {
			return errorResult("output_path must be a relative path without '..' components"), nil
		}
		outputPath = op
	}

	s.log.Infof("Downloading artifact %d to %s", artifactID, outputPath)

	result, err := client.DownloadArtifact(ctx, artifactID, outputPath)
	if err != nil {
		return errorResult(s.formatAuthErrorForRepo(err, fmt.Sprintf("failed to download artifact %d", artifactID), owner, repo)), nil
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
