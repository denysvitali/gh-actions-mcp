package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const workflowRunResourceTemplate = "github-actions://runs/{owner}/{repo}/{run_id}"

// registerResources exposes stable, addressable workflow-run snapshots. A
// client can attach one of these resources to a prompt or conversation without
// first making a tool call just to retrieve the run metadata.
func (s *MCPServer) registerResources() {
	s.srv.AddResourceTemplate(&mcp.ResourceTemplate{
		Name:        "workflow-run",
		Title:       "GitHub Actions workflow run",
		URITemplate: workflowRunResourceTemplate,
		MIMEType:    "application/json",
		Description: "Read the current metadata for a GitHub Actions workflow run. The URI is github-actions://runs/{owner}/{repo}/{run_id}.",
	}, s.readWorkflowRunResource)
}

func (s *MCPServer) readWorkflowRunResource(ctx context.Context, request *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	if request == nil || request.Params == nil {
		return nil, fmt.Errorf("resource URI is required")
	}

	rawURI := request.Params.URI
	u, err := url.Parse(rawURI)
	if err != nil || u.Scheme != "github-actions" || u.Host != "runs" {
		return nil, mcp.ResourceNotFoundError(rawURI)
	}

	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 3 {
		return nil, mcp.ResourceNotFoundError(rawURI)
	}
	owner, err := url.PathUnescape(parts[0])
	if err != nil {
		return nil, mcp.ResourceNotFoundError(rawURI)
	}
	repo, err := url.PathUnescape(parts[1])
	if err != nil {
		return nil, mcp.ResourceNotFoundError(rawURI)
	}
	runID, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || runID <= 0 || owner == "" || repo == "" || strings.ContainsAny(owner+repo, "/\\") {
		return nil, mcp.ResourceNotFoundError(rawURI)
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
	return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
		URI:      rawURI,
		MIMEType: "application/json",
		Text:     string(data),
	}}}, nil
}
