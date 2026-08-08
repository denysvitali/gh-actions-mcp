package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const workflowRunResourceTemplate = "github-actions://runs/{owner}/{repo}/{run_id}"

// registerResources exposes stable, addressable workflow-run snapshots. A
// client can attach one of these resources to a prompt or conversation without
// first making a tool call just to retrieve the run metadata.
func (s *MCPServer) registerResources() {
	s.srv.AddResourceTemplate(&sdkmcp.ResourceTemplate{
		Name:        "workflow-run",
		Title:       "GitHub Actions workflow run",
		URITemplate: workflowRunResourceTemplate,
		MIMEType:    "application/json",
		Description: "Read the current metadata for a GitHub Actions workflow run. The URI is github-actions://runs/{owner}/{repo}/{run_id}.",
	}, s.readWorkflowRunResource)
}

// readWorkflowRunResource serves one workflow-run URI. Every malformed or
// out-of-template URI is reported as "resource not found" rather than as a
// validation error, because to an MCP client a URI it cannot address and a URI
// that does not exist are the same thing.
func (s *MCPServer) readWorkflowRunResource(ctx context.Context, request *sdkmcp.ReadResourceRequest) (*sdkmcp.ReadResourceResult, error) {
	if request == nil || request.Params == nil {
		return nil, fmt.Errorf("resource URI is required")
	}

	rawURI := request.Params.URI
	owner, repo, runID, err := parseWorkflowRunURI(rawURI)
	if err != nil {
		return nil, err
	}

	client, _, _, err := s.clientFromInput(repoInput{Owner: owner, Repo: repo})
	if err != nil {
		return nil, err
	}
	run, err := client.GetWorkflowRun(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("failed to read workflow run %d: %w", runID, err)
	}

	data, err := json.Marshal(run)
	if err != nil {
		return nil, fmt.Errorf("failed to encode workflow run %d: %w", runID, err)
	}
	return &sdkmcp.ReadResourceResult{Contents: []*sdkmcp.ResourceContents{{
		URI:      rawURI,
		MIMEType: "application/json",
		Text:     string(data),
	}}}, nil
}

// parseWorkflowRunURI decomposes github-actions://runs/{owner}/{repo}/{run_id}.
// The owner and repo segments are rejected if they contain a path separator even
// after unescaping, so an encoded separator cannot smuggle extra path segments
// into the API request. Every rejection is a ResourceNotFoundError for rawURI.
func parseWorkflowRunURI(rawURI string) (owner, repo string, runID int64, err error) {
	notFound := func() (string, string, int64, error) {
		return "", "", 0, sdkmcp.ResourceNotFoundError(rawURI)
	}

	u, err := url.Parse(rawURI)
	if err != nil || u.Scheme != "github-actions" || u.Host != "runs" {
		return notFound()
	}

	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 3 {
		return notFound()
	}
	owner, err = url.PathUnescape(parts[0])
	if err != nil {
		return notFound()
	}
	repo, err = url.PathUnescape(parts[1])
	if err != nil {
		return notFound()
	}
	runID, err = strconv.ParseInt(parts[2], 10, 64)
	if err != nil || runID <= 0 || owner == "" || repo == "" || strings.ContainsAny(owner+repo, "/\\") {
		return notFound()
	}
	return owner, repo, runID, nil
}
