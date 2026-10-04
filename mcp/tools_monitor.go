package mcp

import (
	"context"
	"time"

	"github.com/denysvitali/gh-actions-mcp/github"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type verifyInput struct {
	repoInput
	Ref               string   `json:"ref,omitempty"`
	RunID             int64    `json:"run_id,omitempty"`
	ExpectedAttempt   int      `json:"expected_attempt,omitempty"`
	ExpectedWorkflows []string `json:"expected_workflows,omitempty"`
	WaitSeconds       int      `json:"wait_seconds,omitempty"`
	DiagnoseOnFailure bool     `json:"diagnose_on_failure,omitempty"`
}
type watchInput struct {
	Cursor      string `json:"cursor"`
	WaitSeconds int    `json:"wait_seconds,omitempty"`
}

// monitorResult is a bounded snapshot or delta for one immutable target.
type monitorResult struct {
	ExpectedAttempt  int                        `json:"expected_attempt,omitempty"`
	Repository       string                     `json:"repository"`
	SHA              string                     `json:"sha"`
	State            string                     `json:"state"`
	Coverage         string                     `json:"coverage"`
	ObservedAt       string                     `json:"observed_at"`
	Complete         bool                       `json:"complete"`
	StopReason       string                     `json:"stop_reason"`
	TotalWorkflows   int                        `json:"total_workflows"`
	MissingWorkflows []string                   `json:"missing_workflows"`
	Workflows        []monitorRun               `json:"workflows"`
	Jobs             []monitorJob               `json:"jobs"`
	Removed          []string                   `json:"removed,omitempty"`
	Diagnoses        []*github.FailureDiagnosis `json:"diagnoses,omitempty"`
	Warnings         []string                   `json:"warnings,omitempty"`
	Truncated        bool                       `json:"truncated"`
	ChangedOnly      bool                       `json:"changed_only"`
	NextPollSeconds  int                        `json:"next_poll_seconds"`
	Cursor           string                     `json:"cursor"`
}
type monitorRun struct {
	ID         int64  `json:"id"`
	WorkflowID int64  `json:"workflow_id"`
	Attempt    int    `json:"attempt"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion,omitempty"`
}
type monitorJob struct {
	ID         int64  `json:"id"`
	RunID      int64  `json:"run_id"`
	Attempt    int    `json:"attempt"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion,omitempty"`
}

func (s *MCPServer) registerMonitorTools(b toolBuilder) {
	expected := b.property("expected_workflows", "array", b.Description("Workflow IDs or names expected on this commit. Missing workflows keep verification pending."))
	expectedArray := func(d *toolDefinition) {
		expected(d)
		d.properties["expected_workflows"].(map[string]any)["items"] = map[string]any{"type": "string", "maxLength": 256}
		d.properties["expected_workflows"].(map[string]any)["maxItems"] = 50
	}
	addTool(s, b.NewTool("verify_commit", b.WithDescription("Verify an immutable commit or run attempt, optionally diagnose failures, and return a resumable monitoring cursor. Without expected_workflows coverage is observed Actions workflows only, not required branch checks."), b.ReadOnly(), b.repoOverrides(),
		b.WithString("ref", b.Description("Commit SHA, branch, or tag; defaults to local HEAD only for the configured repository.")),
		b.WithNumber("run_id", b.Minimum(1), b.Description("Optional: monitor this run instead of a commit.")),
		b.WithNumber("expected_attempt", b.Minimum(1), b.Description("With run_id, wait for this exact rerun attempt.")),
		expectedArray,
		b.WithNumber("wait_seconds", b.Minimum(0), b.Maximum(30), b.DefaultNumber(0), b.Description("Long-poll budget; 0 returns one snapshot.")),
		b.WithBoolean("diagnose_on_failure", b.Description("Include bounded failure evidence (up to two changed failed workflows).")),
	), s.verifyCommitTyped)
	addTool(s, b.NewTool("watch_ci", b.WithDescription("Resume a pinned CI target using its signed cursor. Returns changed workflows/jobs only, completion evidence, and the next cursor. No remote mutations."), b.ReadOnly(),
		b.WithString("cursor", b.Required(), b.Description("Cursor from verify_commit or watch_ci; expires after 24 hours.")),
		b.WithNumber("wait_seconds", b.Minimum(0), b.Maximum(30), b.DefaultNumber(0)),
	), s.watchCITyped)
}

func (s *MCPServer) verifyCommitTyped(ctx context.Context, _ *sdkmcp.CallToolRequest, in verifyInput) (*sdkmcp.CallToolResult, monitorResult, error) {
	ctx, cancel := monitorCallContext(ctx, in.WaitSeconds)
	defer cancel()
	target, client, err := s.newMonitorTarget(ctx, in)
	if err != nil {
		return nil, monitorResult{}, err
	}
	result, err := s.observeMonitor(ctx, client, target, in.WaitSeconds, false)
	return nil, result, err
}

func (s *MCPServer) watchCITyped(ctx context.Context, _ *sdkmcp.CallToolRequest, in watchInput) (*sdkmcp.CallToolResult, monitorResult, error) {
	ctx, cancel := monitorCallContext(ctx, in.WaitSeconds)
	defer cancel()
	target, err := s.decodeMonitorCursor(in.Cursor)
	if err != nil {
		return nil, monitorResult{}, err
	}
	client, _, _, err := s.clientFromInput(repoInput{Owner: target.Owner, Repo: target.Repo})
	if err != nil {
		return nil, monitorResult{}, err
	}
	result, err := s.observeMonitor(ctx, client, target, in.WaitSeconds, true)
	return nil, result, err
}

func monitorNow() string { return time.Now().UTC().Format(time.RFC3339) }

func monitorCallContext(ctx context.Context, seconds int) (context.Context, context.CancelFunc) {
	if seconds <= 0 {
		seconds = 30
	}
	return context.WithTimeout(ctx, time.Duration(seconds)*time.Second)
}
