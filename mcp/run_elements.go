package mcp

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/denysvitali/gh-actions-mcp/github"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// runElementRequest is the resolved context one get_run element handler needs:
// the client for the (possibly overridden) repository, that repository's
// coordinates for error messages, and the decoded arguments.
type runElementRequest struct {
	client *github.Client
	owner  string
	repo   string
	input  getRunInput
}

// runElementFunc handles a single value of the get_run "element" argument. It is
// written as a method expression on *MCPServer, so handlers stay methods.
type runElementFunc func(*MCPServer, context.Context, runElementRequest) (*sdkmcp.CallToolResult, any, error)

// runElement binds one element name to its handler.
type runElement struct {
	name   string
	handle runElementFunc
}

// runElements is the single source of truth for the get_run "element" argument.
// The schema enum (registerGetRunTool), the validation error message and the
// dispatch in getRunTyped are all derived from this slice, so the three cannot
// drift apart.
//
// The order is part of the MCP wire contract — it is the order of the enum in
// the published schema, pinned by TestGetRunElementEnumOrder — so entries may
// only be appended. "info" is first because it is also the default.
var runElements = []runElement{
	{name: "info", handle: (*MCPServer).runElementInfo},
	{name: "jobs", handle: (*MCPServer).runElementJobs},
	{name: "logs", handle: (*MCPServer).runElementLogs},
	{name: "log_files", handle: (*MCPServer).runElementLogFiles},
	{name: "log_sections", handle: (*MCPServer).runElementLogSections},
	{name: "artifacts", handle: (*MCPServer).runElementArtifacts},
	{name: "artifact_content", handle: (*MCPServer).runElementArtifactContent},
}

// runElementNames lists the valid element names in wire order. Derived from
// runElements; do not edit directly.
var runElementNames = func() []string {
	names := make([]string, 0, len(runElements))
	for _, element := range runElements {
		names = append(names, element.name)
	}
	return names
}()

// runElementHandlers indexes runElements by name for dispatch and validation.
var runElementHandlers = func() map[string]runElementFunc {
	handlers := make(map[string]runElementFunc, len(runElements))
	for _, element := range runElements {
		handlers[element.name] = element.handle
	}
	return handlers
}()

// isValidRunElement reports whether element is a known get_run element.
func isValidRunElement(element string) bool {
	_, ok := runElementHandlers[element]
	return ok
}

// getRunTyped resolves the repository, normalises the element selector and
// dispatches to the handler registered for it. An unknown element is a caller
// error and lists the allowed values.
func (s *MCPServer) getRunTyped(ctx context.Context, _ *sdkmcp.CallToolRequest, input getRunInput) (*sdkmcp.CallToolResult, any, error) {
	client, owner, repo, err := s.clientFromInput(input.repoInput)
	if err != nil {
		return nil, nil, err
	}
	element := strings.ToLower(strings.TrimSpace(input.Element))
	if element == "" {
		element = "info"
	}
	handle, ok := runElementHandlers[element]
	if !ok {
		return nil, nil, fmt.Errorf("invalid element %q. Allowed values: %s", element, strings.Join(runElementNames, ", "))
	}
	return handle(s, ctx, runElementRequest{client: client, owner: owner, repo: repo, input: input})
}

// runElementInfo returns the run's own metadata: the cheapest element, and the
// one callers are told to start with.
func (s *MCPServer) runElementInfo(ctx context.Context, req runElementRequest) (*sdkmcp.CallToolResult, any, error) {
	run, err := req.client.GetWorkflowRun(ctx, req.input.RunID)
	if err != nil {
		return nil, nil, fmt.Errorf("%s", s.formatAuthErrorForRepo(err, fmt.Sprintf("Run ID %d not found", req.input.RunID), req.owner, req.repo))
	}
	if req.input.Format == "full" {
		result, _ := jsonResult(fullRun(run))
		return result, nil, nil
	}
	result, _ := jsonResult(compactRun(run))
	return result, nil, nil
}

// runElementJobs returns the run's jobs, optionally for a specific attempt.
func (s *MCPServer) runElementJobs(ctx context.Context, req runElementRequest) (*sdkmcp.CallToolResult, any, error) {
	attempt := 0
	if req.input.AttemptNumber != nil {
		attempt = int(*req.input.AttemptNumber)
	}
	jobs, err := req.client.GetWorkflowJobs(ctx, req.input.RunID, "", attempt)
	if err != nil {
		return nil, nil, fmt.Errorf("%s", s.formatAuthErrorForRepo(err, fmt.Sprintf("failed to get jobs for run %d", req.input.RunID), req.owner, req.repo))
	}
	return resultForFormat(struct {
		Jobs []*github.Job `json:"jobs"`
	}{Jobs: projectJobs(jobs, req.input.Format)}, req.input.Format), nil, nil
}

// runElementLogFiles lists the log files inside the run archive, optionally
// filtered by a glob on the file path.
func (s *MCPServer) runElementLogFiles(ctx context.Context, req runElementRequest) (*sdkmcp.CallToolResult, any, error) {
	files, err := req.client.GetWorkflowLogFiles(ctx, req.input.RunID)
	if err != nil {
		return nil, nil, fmt.Errorf("%s", s.formatAuthErrorForRepo(err, fmt.Sprintf("failed to get log files for run %d", req.input.RunID), req.owner, req.repo))
	}
	if req.input.FilePattern != "" {
		files, err = filterLogFiles(files, req.input.FilePattern)
		if err != nil {
			return nil, nil, err
		}
	}
	return resultForFormat(struct {
		Files []*github.LogFileInfo `json:"files"`
	}{Files: files}, req.input.Format), nil, nil
}

// filterLogFiles keeps the files whose path matches pattern. An unparsable
// pattern is a caller error, not an empty result.
func filterLogFiles(files []*github.LogFileInfo, pattern string) ([]*github.LogFileInfo, error) {
	filtered := make([]*github.LogFileInfo, 0, len(files))
	for _, file := range files {
		matched, matchErr := filepath.Match(pattern, file.Path)
		if matchErr != nil {
			return nil, fmt.Errorf("invalid file pattern %q: %w", pattern, matchErr)
		}
		if matched {
			filtered = append(filtered, file)
		}
	}
	return filtered, nil
}

// runElementLogSections lists the ##[group] sections of the run's logs, or of a
// single job when job_id is given.
func (s *MCPServer) runElementLogSections(ctx context.Context, req runElementRequest) (*sdkmcp.CallToolResult, any, error) {
	var jobID int64
	if req.input.JobID != nil {
		jobID = *req.input.JobID
	}
	sections, err := req.client.ListLogSections(ctx, req.input.RunID, jobID)
	if err != nil {
		return nil, nil, fmt.Errorf("%s", s.formatAuthErrorForRepo(err, fmt.Sprintf("failed to get log sections for run %d", req.input.RunID), req.owner, req.repo))
	}
	return resultForFormat(struct {
		Sections []*github.LogSection `json:"sections"`
	}{Sections: sections}, req.input.Format), nil, nil
}

// runElementArtifacts lists the run's artifacts without downloading them.
func (s *MCPServer) runElementArtifacts(ctx context.Context, req runElementRequest) (*sdkmcp.CallToolResult, any, error) {
	artifacts, err := req.client.GetWorkflowRunArtifacts(ctx, req.input.RunID)
	if err != nil {
		return nil, nil, fmt.Errorf("%s", s.formatAuthErrorForRepo(err, fmt.Sprintf("failed to get artifacts for run %d", req.input.RunID), req.owner, req.repo))
	}
	return resultForFormat(struct {
		Artifacts []*github.Artifact `json:"artifacts"`
	}{Artifacts: artifacts}, req.input.Format), nil, nil
}

// runElementArtifactContent streams one artifact's contents. Unlike the other
// elements it always renders pretty JSON, because the payload is meant to be
// read by a human or a model rather than parsed.
func (s *MCPServer) runElementArtifactContent(ctx context.Context, req runElementRequest) (*sdkmcp.CallToolResult, any, error) {
	if req.input.ArtifactID == nil {
		return nil, nil, fmt.Errorf("artifact_id is required for element=artifact_content")
	}
	maxSize := req.input.MaxFileSize
	if maxSize <= 0 {
		maxSize = 1024 * 1024
	}
	content, err := req.client.GetArtifactContent(ctx, *req.input.ArtifactID, req.input.FilePattern, maxSize)
	if err != nil {
		return nil, nil, fmt.Errorf("%s", s.formatAuthErrorForRepo(err, fmt.Sprintf("failed to get artifact content %d", *req.input.ArtifactID), req.owner, req.repo))
	}
	result, _ := jsonResultPretty(content)
	return result, nil, nil
}

// runElementLogs returns log text. When the caller applied no limiting argument
// at all the output is auto-truncated to the configured tail length, so that an
// unqualified request cannot flood the context window.
func (s *MCPServer) runElementLogs(ctx context.Context, req runElementRequest) (*sdkmcp.CallToolResult, any, error) {
	input := req.input
	if input.Search != "" && input.SearchRegex != "" {
		return nil, nil, fmt.Errorf("search and search_regex are mutually exclusive")
	}
	filter := &github.LogFilterOptions{Filter: input.Search, FilterRegex: input.SearchRegex, ContextLines: input.Context}
	view := logView{head: intValue(input.Head), tail: intValue(input.Tail), offset: intValue(input.Offset)}
	logs, err := fetchRunLogs(ctx, req, view, filter)
	if err != nil {
		return nil, nil, fmt.Errorf("%s", s.formatAuthErrorForRepo(err, fmt.Sprintf("failed to get logs for run %d", input.RunID), req.owner, req.repo))
	}
	limited := view.head > 0 || view.tail > 0 || input.Search != "" || input.SearchRegex != "" || input.Section != ""
	return truncateLogResult(logs, s.getLogLines(), limited), nil, nil
}

// logView is the caller's line window over a log stream. Zero means "not
// requested" for all three fields, which is what intValue normalises to.
type logView struct {
	head   int
	tail   int
	offset int
}

// fetchRunLogs picks the narrowest log source the arguments allow: a single
// job's live logs (falling back to the run archive once the job's own logs have
// expired), a named section, or the whole run archive.
func fetchRunLogs(ctx context.Context, req runElementRequest, view logView, filter *github.LogFilterOptions) (string, error) {
	input := req.input

	if input.JobID != nil {
		if input.Section != "" {
			return req.client.GetLogSection(ctx, 0, *input.JobID, input.Section, filter)
		}
		logs, err := req.client.GetWorkflowJobLogs(ctx, *input.JobID, view.head, view.tail, view.offset, input.NoHeaders, filter)
		if err == nil {
			return logs, nil
		}
		return req.client.GetWorkflowJobLogsFromRunArchive(ctx, input.RunID, *input.JobID, view.head, view.tail, view.offset, input.NoHeaders, filter)
	}
	if input.Section != "" {
		return req.client.GetLogSection(ctx, input.RunID, 0, input.Section, filter)
	}
	return req.client.GetWorkflowLogsWithPattern(ctx, input.RunID, view.head, view.tail, view.offset, input.NoHeaders, input.FilePattern, filter)
}
