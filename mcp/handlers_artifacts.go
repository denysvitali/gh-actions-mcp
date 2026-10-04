package mcp

import (
	"context"
	"fmt"

	"github.com/denysvitali/gh-actions-mcp/github"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Handlers for the artifact family.

// defaultMaxArtifactFileSize is the per-file read budget used when the caller
// does not specify one. Files larger than this are reported by size only, so a
// single large file cannot fill the response. It matches the schema default
// declared in tools_artifacts.go and tools_runs.go.
const defaultMaxArtifactFileSize = 1024 * 1024

// getArtifactTyped answers get_artifact, streaming contents without writing to
// disk.
func (s *MCPServer) getArtifactTyped(ctx context.Context, _ *sdkmcp.CallToolRequest, input artifactInput) (*sdkmcp.CallToolResult, artifactEvidence, error) {
	var output artifactEvidence
	client, owner, repo, err := s.clientFromInput(input.repoInput)
	if err != nil {
		return nil, output, err
	}
	output, err = s.readArtifactEvidence(ctx, client, owner, repo, input)
	if err != nil {
		return nil, output, fmt.Errorf("%s", s.formatAuthErrorForRepo(err, fmt.Sprintf("failed to get artifact %d", input.ArtifactID), owner, repo))
	}
	return nil, output, nil
}

// downloadArtifactTyped answers download_artifact. Destinations are resolved
// under the configured artifact root, and an existing file is only replaced when
// the caller asked for it.
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
