package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/denysvitali/gh-actions-mcp/github"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Handlers for the workflow/run family. get_run's own dispatch lives in
// run_elements.go.

// listWorkflowsTyped answers list_workflows. It returns one page plus an opaque
// cursor; an empty cursor means there is no further page.
func (s *MCPServer) listWorkflowsTyped(ctx context.Context, _ *sdkmcp.CallToolRequest, input listWorkflowsInput) (*sdkmcp.CallToolResult, listWorkflowsOutput, error) {
	var output listWorkflowsOutput
	client, owner, repo, err := s.clientFromInput(input.repoInput)
	if err != nil {
		return nil, output, err
	}
	limit := input.Limit
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

// listRunsTyped answers list_runs. The returned cursor is bound to the filter
// set: reusing a cursor with different filters is rejected rather than silently
// paginating something else.
func (s *MCPServer) listRunsTyped(ctx context.Context, _ *sdkmcp.CallToolRequest, input listRunsInput) (*sdkmcp.CallToolResult, listRunsOutput, error) {
	var output listRunsOutput
	client, owner, repo, err := s.clientFromInput(input.repoInput)
	if err != nil {
		return nil, output, err
	}
	perPage := input.PerPage
	if perPage <= 0 {
		perPage = s.getLimit()
	}

	workflowID, err := resolveWorkflowFilter(ctx, client, input.WorkflowID)
	if err != nil {
		return nil, output, err
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

// resolveWorkflowFilter turns the polymorphic workflow_id argument into a numeric
// workflow ID, resolving a name or file path against the API when needed. A nil
// result means "no workflow filter", which is not an error.
func resolveWorkflowFilter(ctx context.Context, client *github.Client, raw any) (*int64, error) {
	if raw == nil {
		return nil, nil
	}
	selector, err := normalizeWorkflowIDSelector(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid workflow_id: %w", err)
	}
	if selector == "" {
		return nil, nil
	}
	if resolvedID, parseErr := github.ParseWorkflowID(selector); parseErr == nil {
		return &resolvedID, nil
	}
	resolvedID, _, err := client.ResolveWorkflowID(ctx, selector)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve workflow %q: %w", selector, err)
	}
	return &resolvedID, nil
}

// normalizeWorkflowIDSelector renders the workflow_id argument as a string. The
// argument is schema-typed as "any" because clients send workflow IDs as JSON
// numbers and workflow names as strings; the number forms are normalised to
// their decimal spelling so downstream resolution sees one representation.
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

// fillRunsPage collects up to limit runs starting at position, walking as many
// GitHub API pages as needed. GitHub filters server-side, so one API page can
// yield fewer matches than requested; the returned cursor therefore records both
// the API page and the offset within it. A zero-page cursor means the listing is
// exhausted.
func (s *MCPServer) fillRunsPage(ctx context.Context, client *github.Client, opts *github.ListRunsOptions, position cursorPosition, limit int) ([]*github.WorkflowRun, cursorPosition, error) {
	filler := runsPageFiller{runs: make([]*github.WorkflowRun, 0, limit), limit: limit}
	for position.Page > 0 && len(filler.runs) < limit {
		opts.Page = position.Page
		runs, nextPage, err := client.ListRepositoryWorkflowRunsPage(ctx, opts)
		if err != nil {
			return nil, cursorPosition{}, err
		}
		if position.Offset > len(runs) {
			return nil, cursorPosition{}, fmt.Errorf("cursor offset is no longer valid")
		}
		if resume, full := filler.take(runs, position.Offset, position.Page, nextPage); full {
			return filler.runs, resume, nil
		}
		position = cursorPosition{Page: nextPage}
	}
	return filler.runs, cursorPosition{}, nil
}

// runsPageFiller accumulates runs across GitHub API pages.
type runsPageFiller struct {
	runs  []*github.WorkflowRun
	limit int
}

// take appends page[offset:] until the filler holds limit runs. It reports
// whether the limit was reached and, if so, where a follow-up call must resume:
// the same API page when items are left over, otherwise the next page.
func (f *runsPageFiller) take(page []*github.WorkflowRun, offset, pageNumber, nextPage int) (cursorPosition, bool) {
	for index := offset; index < len(page); index++ {
		f.runs = append(f.runs, page[index])
		if len(f.runs) < f.limit {
			continue
		}
		if index+1 < len(page) {
			return cursorPosition{Page: pageNumber, Offset: index + 1}, true
		}
		return cursorPosition{Page: nextPage}, true
	}
	return cursorPosition{}, false
}

// formatRuns projects runs onto the requested verbosity. An unknown format is
// treated as "compact", which is also the schema default.
func formatRuns(runs []*github.WorkflowRun, format string) []any {
	items := make([]any, 0, len(runs))
	for _, run := range runs {
		switch format {
		case "minimal":
			minimal := minimalRun(run)
			items = append(items, &minimal)
		case "full":
			items = append(items, fullRun(run))
		default:
			items = append(items, compactRun(run))
		}
	}
	return items
}

// manageRunTyped answers manage_run, the one tool that mutates a run.
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
