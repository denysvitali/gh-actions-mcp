package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/denysvitali/gh-actions-mcp/github"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Handlers for the read-only analysis family: analyze_timing, get_check_status
// and diagnose_failure.

// analyzeTimingTyped answers analyze_timing. Either a workflow selector or a
// run_id is required, and step_name is only meaningful inside a job_name.
func (s *MCPServer) analyzeTimingTyped(ctx context.Context, _ *sdkmcp.CallToolRequest, input analyzeTimingInput) (*sdkmcp.CallToolResult, github.TimingAnalysis, error) {
	var output github.TimingAnalysis
	client, owner, repo, err := s.clientFromInput(input.repoInput)
	if err != nil {
		return nil, output, err
	}
	workflow, job, step := strings.TrimSpace(input.Workflow), strings.TrimSpace(input.JobName), strings.TrimSpace(input.StepName)
	if step != "" && job == "" {
		return nil, output, fmt.Errorf("job_name is required when step_name is provided")
	}
	var runID int64
	if input.RunID != nil {
		runID = *input.RunID
	}
	if workflow == "" && runID == 0 {
		return nil, output, fmt.Errorf("workflow or run_id is required")
	}
	limit := input.Limit
	if limit <= 0 {
		limit = 10
	}
	analysis, err := client.AnalyzeTiming(ctx, &github.TimingAnalysisOptions{Workflow: workflow, RunID: runID, Branch: strings.TrimSpace(input.Branch), JobName: job, StepName: step, Conclusion: strings.TrimSpace(input.Conclusion), Limit: limit})
	if err != nil {
		return nil, output, fmt.Errorf("%s", s.formatAuthErrorForRepo(err, "failed to analyze timing", owner, repo))
	}
	return nil, *analysis, nil
}

// getCheckStatusTyped answers get_check_status. The default "summary" format
// returns prose; "compact" and "full" return the raw JSON rollup.
func (s *MCPServer) getCheckStatusTyped(ctx context.Context, _ *sdkmcp.CallToolRequest, input checkStatusInput) (*sdkmcp.CallToolResult, any, error) {
	client, owner, repo, err := s.clientFromInput(input.repoInput)
	if err != nil {
		return nil, nil, err
	}
	ref, err := s.resolveCheckRef(input.Ref, owner, repo)
	if err != nil {
		return nil, nil, err
	}
	filter := strings.ToLower(strings.TrimSpace(input.Filter))
	if filter == "" {
		filter = "latest"
	}
	status, err := client.GetCheckRunsForRef(ctx, ref, &github.GetCheckRunsOptions{CheckName: input.CheckName, Status: input.Status, Filter: filter})
	if err != nil {
		return nil, nil, fmt.Errorf("%s", s.formatAuthErrorForRepo(err, "failed to get check status", owner, repo))
	}
	if input.Format == "compact" || input.Format == "full" {
		result, _ := jsonResult(status)
		return result, nil, nil
	}
	return textResult(formatWorkflowStatusSummary(ref, status, filter)), nil, nil
}

// resolveCheckRef falls back to the local HEAD commit when no ref was given.
// That inference is only valid when the call targets the configured repository:
// the local checkout says nothing about any other repository, and a failure to
// read it is not fatal, it just leaves the ref empty.
func (s *MCPServer) resolveCheckRef(rawRef, owner, repo string) (string, error) {
	ref := strings.TrimSpace(rawRef)
	if ref == "" && owner == s.config.RepoOwner && repo == s.config.RepoName {
		if commit, commitErr := github.GetLastCommit(); commitErr == nil {
			ref = commit.SHA
		}
	}
	if ref == "" {
		return "", fmt.Errorf("ref is required when it cannot be inferred from the configured repository")
	}
	return ref, nil
}

// diagnoseFailureTyped answers diagnose_failure. With no run_id it diagnoses the
// most recent failed run, which is the common "why is CI red" case.
func (s *MCPServer) diagnoseFailureTyped(ctx context.Context, _ *sdkmcp.CallToolRequest, input diagnoseInput) (*sdkmcp.CallToolResult, github.FailureDiagnosis, error) {
	var output github.FailureDiagnosis
	client, owner, repo, err := s.clientFromInput(input.repoInput)
	if err != nil {
		return nil, output, err
	}
	checkFlakiness := input.CheckFlakiness == nil || *input.CheckFlakiness
	maxLines := input.MaxErrorLines
	if maxLines <= 0 {
		maxLines = 50
	}
	var runID int64
	if input.RunID != nil {
		runID = *input.RunID
	} else {
		runID, err = s.latestFailedRunID(ctx, client, owner, repo)
		if err != nil {
			return nil, output, err
		}
	}
	diagnosis, err := client.DiagnoseFailure(ctx, runID, checkFlakiness, maxLines)
	if err != nil {
		return nil, output, fmt.Errorf("%s", s.formatAuthErrorForRepo(err, fmt.Sprintf("failed to diagnose run %d", runID), owner, repo))
	}
	return nil, *diagnosis, nil
}

// latestFailedRunID finds the newest completed-and-failed run to diagnose. It
// restricts the search to the current local branch only when the call targets the
// configured repository, for the same reason as resolveCheckRef.
func (s *MCPServer) latestFailedRunID(ctx context.Context, client *github.Client, owner, repo string) (int64, error) {
	branch := ""
	if owner == s.config.RepoOwner && repo == s.config.RepoName {
		branch, _ = github.GetCurrentBranch()
	}
	runs, listErr := client.ListRepositoryWorkflowRunsWithOptions(ctx, &github.ListRunsOptions{Per_page: 5, Status: "completed", Conclusion: "failure", Branch: branch})
	if listErr != nil {
		return 0, fmt.Errorf("%s", s.formatAuthErrorForRepo(listErr, "failed to find failed runs", owner, repo))
	}
	if len(runs) == 0 {
		return 0, fmt.Errorf("no failed runs found")
	}
	return runs[0].ID, nil
}
