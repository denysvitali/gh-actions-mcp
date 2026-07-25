package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/denysvitali/gh-actions-mcp/github"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *MCPServer) clientFromInput(input repoInput) (*github.Client, string, string, error) {
	owner := strings.TrimSpace(input.Owner)
	repo := strings.TrimSpace(input.Repo)
	if owner == "" {
		owner = s.config.RepoOwner
	}
	if repo == "" {
		repo = s.config.RepoName
	}
	if strings.Contains(repo, "/") {
		parts := strings.SplitN(repo, "/", 2)
		if parts[0] != "" && parts[1] != "" {
			owner, repo = parts[0], parts[1]
		}
	}
	if owner == "" || repo == "" {
		return nil, "", "", fmt.Errorf("repository owner/repo not set. Provide owner and repo arguments")
	}
	perPage := s.config.PerPageLimit
	if perPage <= 0 {
		perPage = 50
	}
	client, err := github.NewClientWithOptions(github.ClientOptions{
		Token: s.config.Token, Owner: owner, Repo: repo, PerPageLimit: perPage,
		APIBaseURL: s.config.APIBaseURL, UploadURL: s.config.UploadURL, RetryMax: s.config.RetryMax,
		AuthUsername: s.config.AuthUsername,
	})
	return client, owner, repo, err
}

func (s *MCPServer) listWorkflowsTyped(ctx context.Context, _ *sdkmcp.CallToolRequest, input listWorkflowsInput) (*sdkmcp.CallToolResult, listWorkflowsOutput, error) {
	var output listWorkflowsOutput
	client, owner, repo, err := s.clientFromInput(input.repoInput)
	if err != nil {
		return nil, output, err
	}
	limit := int(input.Limit)
	if limit <= 0 {
		limit = s.getLimit()
	}
	scope := struct {
		Owner, Repo string
		Limit       int
		Format      string
	}{owner, repo, limit, input.Format}
	position, err := decodeCursor(input.Cursor, scope, s.cursorKey())
	if err != nil {
		return nil, output, err
	}
	workflows, nextPage, err := client.ListWorkflowsPage(ctx, limit, position.Page)
	if err != nil {
		return nil, output, fmt.Errorf("%s", s.formatAuthErrorForRepo(err, "failed to list workflows", owner, repo))
	}
	nextCursor, err := encodeCursor(cursorPosition{Page: nextPage}, scope, s.cursorKey())
	if err != nil {
		return nil, output, err
	}
	output = listWorkflowsOutput{Workflows: workflows, NextCursor: nextCursor}
	return nil, output, nil
}

func (s *MCPServer) listRunsTyped(ctx context.Context, _ *sdkmcp.CallToolRequest, input listRunsInput) (*sdkmcp.CallToolResult, listRunsOutput, error) {
	var output listRunsOutput
	client, owner, repo, err := s.clientFromInput(input.repoInput)
	if err != nil {
		return nil, output, err
	}
	perPage := int(input.PerPage)
	if perPage <= 0 {
		perPage = s.getLimit()
	}

	var workflowID *int64
	if input.WorkflowID != nil {
		selector, err := normalizeWorkflowIDSelector(input.WorkflowID)
		if err != nil {
			return nil, output, fmt.Errorf("invalid workflow_id: %w", err)
		}
		if selector != "" {
			resolvedID, parseErr := github.ParseWorkflowID(selector)
			if parseErr == nil {
				workflowID = &resolvedID
			} else {
				var err error
				resolvedID, _, err = client.ResolveWorkflowID(ctx, selector)
				if err != nil {
					return nil, output, fmt.Errorf("failed to resolve workflow %q: %w", selector, err)
				}
				workflowID = &resolvedID
			}
		}
	}

	scope := struct {
		Owner, Repo, Branch, Status, Conclusion, CreatedAfter, Event, Actor, Format string
		WorkflowID                                                                  *int64
		PerPage                                                                     int
	}{owner, repo, input.Branch, input.Status, input.Conclusion, input.CreatedAfter, input.Event, input.Actor, input.Format, workflowID, perPage}
	position, err := decodeCursor(input.Cursor, scope, s.cursorKey())
	if err != nil {
		return nil, output, err
	}
	opts := &github.ListRunsOptions{
		WorkflowID: workflowID, Branch: input.Branch, Status: input.Status,
		Conclusion: input.Conclusion, Per_page: perPage, CreatedAfter: input.CreatedAfter,
		Event: input.Event, Actor: input.Actor, Page: position.Page,
	}
	runs, nextPosition, err := s.fillRunsPage(ctx, client, opts, position, perPage)
	if err != nil {
		return nil, output, fmt.Errorf("%s", s.formatAuthErrorForRepo(err, "failed to list workflow runs", owner, repo))
	}
	nextCursor, err := encodeCursor(nextPosition, scope, s.cursorKey())
	if err != nil {
		return nil, output, err
	}
	output = listRunsOutput{Runs: formatRuns(runs, input.Format), NextCursor: nextCursor}
	return nil, output, nil
}

func normalizeWorkflowIDSelector(raw any) (string, error) {
	if raw == nil {
		return "", nil
	}
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v), nil
	case json.Number:
		str := strings.TrimSpace(string(v))
		if str == "" {
			return "", nil
		}
		if _, err := strconv.ParseInt(str, 10, 64); err != nil {
			return "", fmt.Errorf("workflow_id %q is not a valid integer", str)
		}
		return str, nil
	case float64:
		if v != float64(int64(v)) {
			return "", fmt.Errorf("workflow_id %v is not an integer", v)
		}
		return strconv.FormatInt(int64(v), 10), nil
	case int:
		return strconv.FormatInt(int64(v), 10), nil
	case int64:
		return strconv.FormatInt(v, 10), nil
	case uint64:
		return strconv.FormatUint(v, 10), nil
	default:
		return "", fmt.Errorf("workflow_id must be an integer or a workflow name/path")
	}
}

func (s *MCPServer) cursorKey() []byte {
	digest := sha256.Sum256([]byte("github-actions-mcp cursor v1\x00" + s.config.Token))
	return digest[:]
}

func (s *MCPServer) fillRunsPage(ctx context.Context, client *github.Client, opts *github.ListRunsOptions, position cursorPosition, limit int) ([]*github.WorkflowRun, cursorPosition, error) {
	result := make([]*github.WorkflowRun, 0, limit)
	for position.Page > 0 && len(result) < limit {
		opts.Page = position.Page
		runs, nextPage, err := client.ListRepositoryWorkflowRunsPage(ctx, opts)
		if err != nil {
			return nil, cursorPosition{}, err
		}
		if position.Offset > len(runs) {
			return nil, cursorPosition{}, fmt.Errorf("cursor offset is no longer valid")
		}
		for index := position.Offset; index < len(runs); index++ {
			result = append(result, runs[index])
			if len(result) == limit {
				if index+1 < len(runs) {
					return result, cursorPosition{Page: position.Page, Offset: index + 1}, nil
				}
				return result, cursorPosition{Page: nextPage}, nil
			}
		}
		position = cursorPosition{Page: nextPage}
	}
	return result, cursorPosition{}, nil
}

func formatRuns(runs []*github.WorkflowRun, format string) []any {
	items := make([]any, 0, len(runs))
	for _, run := range runs {
		switch format {
		case "minimal":
			items = append(items, &github.WorkflowRunMinimal{ID: run.ID, Name: run.Name, Status: run.Status, Conclusion: run.Conclusion, CreatedAt: run.CreatedAt, DurationSeconds: run.DurationSeconds})
		case "full":
			items = append(items, &github.WorkflowRunFull{ID: run.ID, Name: run.Name, Status: run.Status, Conclusion: run.Conclusion, Branch: run.Branch, Event: run.Event, Actor: run.Actor, CreatedAt: run.CreatedAt, UpdatedAt: run.UpdatedAt, URL: run.URL, RunNumber: run.RunNumber, WorkflowID: run.WorkflowID, HeadSHA: run.HeadSHA, StartedAt: run.StartedAt, CompletedAt: run.UpdatedAt, DurationSeconds: run.DurationSeconds})
		default:
			items = append(items, &github.WorkflowRunCompact{WorkflowRunMinimal: github.WorkflowRunMinimal{ID: run.ID, Name: run.Name, Status: run.Status, Conclusion: run.Conclusion, CreatedAt: run.CreatedAt, DurationSeconds: run.DurationSeconds}, Branch: run.Branch, SHA: run.HeadSHA, Event: run.Event, Actor: run.Actor, URL: run.URL})
		}
	}
	return items
}

func (s *MCPServer) getRunTyped(ctx context.Context, _ *sdkmcp.CallToolRequest, input getRunInput) (*sdkmcp.CallToolResult, any, error) {
	client, owner, repo, err := s.clientFromInput(input.repoInput)
	if err != nil {
		return nil, nil, err
	}
	element := strings.ToLower(strings.TrimSpace(input.Element))
	if element == "" {
		element = "info"
	}
	if !isValidRunElement(element) {
		return nil, nil, fmt.Errorf("invalid element %q. Allowed values: %s", element, strings.Join(validRunElements, ", "))
	}
	switch element {
	case "jobs":
		attempt := 0
		if input.AttemptNumber != nil {
			attempt = int(*input.AttemptNumber)
		}
		jobs, err := client.GetWorkflowJobs(ctx, input.RunID, "", attempt)
		if err != nil {
			return nil, nil, fmt.Errorf("%s", s.formatAuthErrorForRepo(err, fmt.Sprintf("failed to get jobs for run %d", input.RunID), owner, repo))
		}
		return resultForFormat(struct {
			Jobs []*github.Job `json:"jobs"`
		}{Jobs: jobs}, input.Format), nil, nil
	case "logs":
		return s.getRunLogsTyped(ctx, client, owner, repo, input)
	case "log_files":
		files, err := client.GetWorkflowLogFiles(ctx, input.RunID)
		if err != nil {
			return nil, nil, fmt.Errorf("%s", s.formatAuthErrorForRepo(err, fmt.Sprintf("failed to get log files for run %d", input.RunID), owner, repo))
		}
		if input.FilePattern != "" {
			filtered := make([]*github.LogFileInfo, 0, len(files))
			for _, file := range files {
				matched, matchErr := filepath.Match(input.FilePattern, file.Path)
				if matchErr != nil {
					return nil, nil, fmt.Errorf("invalid file pattern %q: %w", input.FilePattern, matchErr)
				}
				if matched {
					filtered = append(filtered, file)
				}
			}
			files = filtered
		}
		return resultForFormat(struct {
			Files []*github.LogFileInfo `json:"files"`
		}{Files: files}, input.Format), nil, nil
	case "log_sections":
		var jobID int64
		if input.JobID != nil {
			jobID = *input.JobID
		}
		sections, err := client.ListLogSections(ctx, input.RunID, jobID)
		if err != nil {
			return nil, nil, fmt.Errorf("%s", s.formatAuthErrorForRepo(err, fmt.Sprintf("failed to get log sections for run %d", input.RunID), owner, repo))
		}
		return resultForFormat(struct {
			Sections []*github.LogSection `json:"sections"`
		}{Sections: sections}, input.Format), nil, nil
	case "artifacts":
		artifacts, err := client.GetWorkflowRunArtifacts(ctx, input.RunID)
		if err != nil {
			return nil, nil, fmt.Errorf("%s", s.formatAuthErrorForRepo(err, fmt.Sprintf("failed to get artifacts for run %d", input.RunID), owner, repo))
		}
		return resultForFormat(struct {
			Artifacts []*github.Artifact `json:"artifacts"`
		}{Artifacts: artifacts}, input.Format), nil, nil
	case "artifact_content":
		if input.ArtifactID == nil {
			return nil, nil, fmt.Errorf("artifact_id is required for element=artifact_content")
		}
		maxSize := int64(input.MaxFileSize)
		if maxSize <= 0 {
			maxSize = 1024 * 1024
		}
		content, err := client.GetArtifactContent(ctx, *input.ArtifactID, input.FilePattern, maxSize)
		if err != nil {
			return nil, nil, fmt.Errorf("%s", s.formatAuthErrorForRepo(err, fmt.Sprintf("failed to get artifact content %d", *input.ArtifactID), owner, repo))
		}
		result, _ := jsonResultPretty(content)
		return result, nil, nil
	default:
		run, err := client.GetWorkflowRun(ctx, input.RunID)
		if err != nil {
			return nil, nil, fmt.Errorf("%s", s.formatAuthErrorForRepo(err, fmt.Sprintf("Run ID %d not found", input.RunID), owner, repo))
		}
		if input.Format == "full" {
			result, _ := jsonResult(&github.WorkflowRunFull{ID: run.ID, Name: run.Name, Status: run.Status, Conclusion: run.Conclusion, Branch: run.Branch, Event: run.Event, Actor: run.Actor, CreatedAt: run.CreatedAt, UpdatedAt: run.UpdatedAt, URL: run.URL, RunNumber: run.RunNumber, WorkflowID: run.WorkflowID, HeadSHA: run.HeadSHA, StartedAt: run.StartedAt, CompletedAt: run.UpdatedAt, DurationSeconds: run.DurationSeconds})
			return result, nil, nil
		}
		result, _ := jsonResult(&github.WorkflowRunCompact{WorkflowRunMinimal: github.WorkflowRunMinimal{ID: run.ID, Name: run.Name, Status: run.Status, Conclusion: run.Conclusion, CreatedAt: run.CreatedAt, DurationSeconds: run.DurationSeconds}, Branch: run.Branch, SHA: run.HeadSHA, Event: run.Event, Actor: run.Actor, URL: run.URL})
		return result, nil, nil
	}
}

func resultForFormat(value any, format string) *sdkmcp.CallToolResult {
	if format == "full" || format == "pretty" {
		result, _ := jsonResultPretty(value)
		return result
	}
	result, _ := jsonResult(value)
	return result
}

func intValue(value *int64) int {
	if value == nil || *value <= 0 {
		return 0
	}
	return int(*value)
}

func (s *MCPServer) getRunLogsTyped(ctx context.Context, client *github.Client, owner, repo string, input getRunInput) (*sdkmcp.CallToolResult, any, error) {
	if input.Search != "" && input.SearchRegex != "" {
		return nil, nil, fmt.Errorf("search and search_regex are mutually exclusive")
	}
	filter := &github.LogFilterOptions{Filter: input.Search, FilterRegex: input.SearchRegex, ContextLines: int(input.Context)}
	head, tail, offset := intValue(input.Head), intValue(input.Tail), intValue(input.Offset)
	var logs string
	var err error
	if input.JobID != nil {
		if input.Section != "" {
			logs, err = client.GetLogSection(ctx, 0, *input.JobID, input.Section, filter)
		} else {
			logs, err = client.GetWorkflowJobLogs(ctx, *input.JobID, head, tail, offset, input.NoHeaders, filter)
			if err != nil {
				logs, err = client.GetWorkflowJobLogsFromRunArchive(ctx, input.RunID, *input.JobID, head, tail, offset, input.NoHeaders, filter)
			}
		}
	} else if input.Section != "" {
		logs, err = client.GetLogSection(ctx, input.RunID, 0, input.Section, filter)
	} else {
		logs, err = client.GetWorkflowLogsWithPattern(ctx, input.RunID, head, tail, offset, input.NoHeaders, input.FilePattern, filter)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("%s", s.formatAuthErrorForRepo(err, fmt.Sprintf("failed to get logs for run %d", input.RunID), owner, repo))
	}
	limited := head > 0 || tail > 0 || input.Search != "" || input.SearchRegex != "" || input.Section != ""
	return truncateLogResult(logs, s.getLogLines(), limited), nil, nil
}

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
	limit := int(input.Limit)
	if limit <= 0 {
		limit = 10
	}
	analysis, err := client.AnalyzeTiming(ctx, &github.TimingAnalysisOptions{Workflow: workflow, RunID: runID, Branch: strings.TrimSpace(input.Branch), JobName: job, StepName: step, Conclusion: strings.TrimSpace(input.Conclusion), Limit: limit})
	if err != nil {
		return nil, output, fmt.Errorf("%s", s.formatAuthErrorForRepo(err, "failed to analyze timing", owner, repo))
	}
	return nil, *analysis, nil
}

func (s *MCPServer) getCheckStatusTyped(ctx context.Context, _ *sdkmcp.CallToolRequest, input checkStatusInput) (*sdkmcp.CallToolResult, any, error) {
	client, owner, repo, err := s.clientFromInput(input.repoInput)
	if err != nil {
		return nil, nil, err
	}
	ref := strings.TrimSpace(input.Ref)
	if ref == "" && owner == s.config.RepoOwner && repo == s.config.RepoName {
		if commit, commitErr := github.GetLastCommit(); commitErr == nil {
			ref = commit.SHA
		}
	}
	if ref == "" {
		return nil, nil, fmt.Errorf("ref is required when it cannot be inferred from the configured repository")
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

func (s *MCPServer) waitForRunTyped(ctx context.Context, request *sdkmcp.CallToolRequest, input waitRunInput) (*sdkmcp.CallToolResult, github.WaitRunResult, error) {
	return s.waitRunTyped(ctx, request, input, false)
}

func (s *MCPServer) waitAllTyped(ctx context.Context, request *sdkmcp.CallToolRequest, input waitRunInput) (*sdkmcp.CallToolResult, github.WaitRunResult, error) {
	return s.waitRunTyped(ctx, request, input, true)
}

func (s *MCPServer) waitRunTyped(ctx context.Context, _ *sdkmcp.CallToolRequest, input waitRunInput, all bool) (*sdkmcp.CallToolResult, github.WaitRunResult, error) {
	var output github.WaitRunResult
	client, owner, repo, err := s.clientFromInput(input.repoInput)
	if err != nil {
		return nil, output, err
	}
	timeout := int(input.TimeoutMinutes)
	if timeout <= 0 {
		timeout = 30
	}
	var result *github.WaitRunResult
	if all {
		result, err = client.WaitForAll(ctx, input.RunID, timeout)
	} else {
		result, err = client.WaitForRun(ctx, input.RunID, timeout)
	}
	if result != nil {
		output = *result
	}
	if err != nil && (result == nil || !result.TimeoutReached) {
		return nil, output, fmt.Errorf("%s", s.formatAuthErrorForRepo(err, "failed to wait for run", owner, repo))
	}
	return nil, output, nil
}

func (s *MCPServer) waitForCommitChecksTyped(ctx context.Context, _ *sdkmcp.CallToolRequest, input waitChecksInput) (*sdkmcp.CallToolResult, github.WaitCommitChecksResult, error) {
	var output github.WaitCommitChecksResult
	client, owner, repo, err := s.clientFromInput(input.repoInput)
	if err != nil {
		return nil, output, err
	}
	timeout := int(input.TimeoutMinutes)
	if timeout <= 0 {
		timeout = 30
	}
	result, err := client.WaitForCommitChecks(ctx, strings.TrimSpace(input.Ref), timeout)
	if result != nil {
		output = *result
	}
	if err != nil && (result == nil || !result.TimeoutReached) {
		return nil, output, fmt.Errorf("%s", s.formatAuthErrorForRepo(err, "failed to wait for checks", owner, repo))
	}
	return nil, output, nil
}

func (s *MCPServer) manageRunTyped(ctx context.Context, _ *sdkmcp.CallToolRequest, input manageRunInput) (*sdkmcp.CallToolResult, github.ManageRunResult, error) {
	var output github.ManageRunResult
	client, owner, repo, err := s.clientFromInput(input.repoInput)
	if err != nil {
		return nil, output, err
	}
	result, err := client.ManageRun(ctx, input.RunID, github.ManageRunAction(input.Action))
	if err != nil {
		return nil, output, fmt.Errorf("%s", s.formatAuthErrorForRepo(err, "failed to manage run", owner, repo))
	}
	return nil, *result, nil
}

func (s *MCPServer) getArtifactTyped(ctx context.Context, _ *sdkmcp.CallToolRequest, input artifactInput) (*sdkmcp.CallToolResult, github.ArtifactContent, error) {
	var output github.ArtifactContent
	client, owner, repo, err := s.clientFromInput(input.repoInput)
	if err != nil {
		return nil, output, err
	}
	maxSize := int64(input.MaxFileSize)
	if maxSize <= 0 {
		maxSize = 1024 * 1024
	}
	content, err := client.GetArtifactContent(ctx, input.ArtifactID, input.FilePattern, maxSize)
	if err != nil {
		return nil, output, fmt.Errorf("%s", s.formatAuthErrorForRepo(err, fmt.Sprintf("failed to get artifact %d", input.ArtifactID), owner, repo))
	}
	return nil, *content, nil
}

func (s *MCPServer) diagnoseFailureTyped(ctx context.Context, _ *sdkmcp.CallToolRequest, input diagnoseInput) (*sdkmcp.CallToolResult, github.FailureDiagnosis, error) {
	var output github.FailureDiagnosis
	client, owner, repo, err := s.clientFromInput(input.repoInput)
	if err != nil {
		return nil, output, err
	}
	checkFlakiness := input.CheckFlakiness == nil || *input.CheckFlakiness
	maxLines := int(input.MaxErrorLines)
	if maxLines <= 0 {
		maxLines = 50
	}
	var runID int64
	if input.RunID != nil {
		runID = *input.RunID
	} else {
		branch := ""
		if owner == s.config.RepoOwner && repo == s.config.RepoName {
			branch, _ = github.GetCurrentBranch()
		}
		runs, listErr := client.ListRepositoryWorkflowRunsWithOptions(ctx, &github.ListRunsOptions{Per_page: 5, Status: "completed", Conclusion: "failure", Branch: branch})
		if listErr != nil {
			return nil, output, fmt.Errorf("%s", s.formatAuthErrorForRepo(listErr, "failed to find failed runs", owner, repo))
		}
		if len(runs) == 0 {
			return nil, output, fmt.Errorf("no failed runs found")
		}
		runID = runs[0].ID
	}
	diagnosis, err := client.DiagnoseFailure(ctx, runID, checkFlakiness, maxLines)
	if err != nil {
		return nil, output, fmt.Errorf("%s", s.formatAuthErrorForRepo(err, fmt.Sprintf("failed to diagnose run %d", runID), owner, repo))
	}
	return nil, *diagnosis, nil
}

func (s *MCPServer) downloadArtifactTyped(ctx context.Context, _ *sdkmcp.CallToolRequest, input downloadArtifactInput) (*sdkmcp.CallToolResult, github.ArtifactDownloadResult, error) {
	var output github.ArtifactDownloadResult
	client, owner, repo, err := s.clientFromInput(input.repoInput)
	if err != nil {
		return nil, output, err
	}
	result, err := client.DownloadArtifactWithOptions(ctx, input.ArtifactID, github.ArtifactDownloadOptions{
		Root: s.config.ArtifactRoot, OutputPath: input.OutputPath, Overwrite: input.Overwrite,
	})
	if err != nil {
		return nil, output, fmt.Errorf("%s", s.formatAuthErrorForRepo(err, fmt.Sprintf("failed to download artifact %d", input.ArtifactID), owner, repo))
	}
	return nil, *result, nil
}
