package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/denysvitali/gh-actions-mcp/github"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Handlers for the polling family. All of them share one rule about timeouts: a
// timeout is not an error. The result carries TimeoutReached and the tool still
// succeeds, so the caller can decide whether to keep waiting.

// defaultWaitTimeoutMinutes is the polling budget used when the caller does not
// specify one. It matches the schema default declared in tools_wait.go.
const defaultWaitTimeoutMinutes = 30

// waitTarget names what a wait tool polls until completion.
type waitTarget int

const (
	// waitTargetRun ends on terminal run status or an observed failure.
	waitTargetRun waitTarget = iota
	// waitTargetAllJobs requires terminal run status and every job completed.
	waitTargetAllJobs
)

// waitForRunTyped answers wait_for_run.
func (s *MCPServer) waitForRunTyped(ctx context.Context, request *sdkmcp.CallToolRequest, input waitRunInput) (*sdkmcp.CallToolResult, github.WaitRunResult, error) {
	return s.waitRunTyped(ctx, request, input, waitTargetRun)
}

// waitAllTyped answers wait_all.
func (s *MCPServer) waitAllTyped(ctx context.Context, request *sdkmcp.CallToolRequest, input waitRunInput) (*sdkmcp.CallToolResult, github.WaitRunResult, error) {
	return s.waitRunTyped(ctx, request, input, waitTargetAllJobs)
}

// waitRunTyped implements both run-oriented wait tools. A partial result is
// returned even on error, so a caller that times out still sees what was
// observed.
func (s *MCPServer) waitRunTyped(ctx context.Context, _ *sdkmcp.CallToolRequest, input waitRunInput, target waitTarget) (*sdkmcp.CallToolResult, github.WaitRunResult, error) {
	var output github.WaitRunResult
	client, owner, repo, err := s.clientFromInput(input.repoInput)
	if err != nil {
		return nil, output, err
	}
	timeout := input.TimeoutMinutes
	if timeout <= 0 {
		timeout = defaultWaitTimeoutMinutes
	}
	result, err := client.WaitForRunAttempt(ctx, input.RunID, timeout, input.ExpectedAttempt, target == waitTargetAllJobs)
	if result != nil {
		output = *result
	}
	if err != nil && (result == nil || !result.TimeoutReached) {
		return nil, output, fmt.Errorf("%s", s.formatAuthErrorForRepo(err, "failed to wait for run", owner, repo))
	}
	return nil, output, nil
}

// waitForCommitChecksTyped answers wait_for_commit_checks, which waits on a ref
// rather than on a single run.
func (s *MCPServer) waitForCommitChecksTyped(ctx context.Context, _ *sdkmcp.CallToolRequest, input waitChecksInput) (*sdkmcp.CallToolResult, github.WaitCommitChecksResult, error) {
	var output github.WaitCommitChecksResult
	client, owner, repo, err := s.clientFromInput(input.repoInput)
	if err != nil {
		return nil, output, err
	}
	timeout := input.TimeoutMinutes
	if timeout <= 0 {
		timeout = defaultWaitTimeoutMinutes
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
