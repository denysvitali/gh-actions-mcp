package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/denysvitali/gh-actions-mcp/config"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

const monitorTestSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func monitorTestServer(t *testing.T, handler http.Handler) *MCPServer {
	t.Helper()
	api := httptest.NewServer(handler)
	t.Cleanup(api.Close)
	server, err := NewMCPServer(&config.Config{Token: "test-token", RepoOwner: "o", RepoName: "r", APIBaseURL: api.URL + "/", RetryMax: -1}, logrus.New())
	require.NoError(t, err)
	t.Cleanup(func() { _ = server.Close() })
	return server
}

func readMonitorResult(t *testing.T, r *sdkmcp.CallToolResult, err error) monitorResult {
	t.Helper()
	require.NoError(t, err)
	require.False(t, r.IsError, fmt.Sprint(r.Content))
	data, err := json.Marshal(r.StructuredContent)
	require.NoError(t, err)
	var out monitorResult
	require.NoError(t, json.Unmarshal(data, &out))
	return out
}

func TestMonitorPinsRefAndReturnsDeltas(t *testing.T) {
	var finished atomic.Bool
	var resolves atomic.Int32
	server := monitorTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/o/r/commits/main":
			resolves.Add(1)
			fmt.Fprintf(w, `{"sha":%q}`, monitorTestSHA)
		case "/repos/o/r/actions/runs":
			require.Equal(t, monitorTestSHA, r.URL.Query().Get("head_sha"))
			status, conclusion := "in_progress", ""
			if finished.Load() {
				status, conclusion = "completed", "success"
			}
			fmt.Fprintf(w, `{"total_count":1,"workflow_runs":[{"id":1,"workflow_id":10,"run_attempt":1,"name":"CI","head_sha":%q,"status":%q,"conclusion":%q}]}`, monitorTestSHA, status, conclusion)
		case "/repos/o/r/actions/runs/1/attempts/1/jobs":
			fmt.Fprint(w, `{"total_count":1,"jobs":[{"id":2,"run_id":1,"run_attempt":1,"name":"test","status":"in_progress"}]}`)
		default:
			t.Errorf("unexpected API request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	r, err := server.InvokeTool(context.Background(), "verify_commit", map[string]any{"ref": "main", "expected_workflows": []string{"CI", "Build"}})
	initial := readMonitorResult(t, r, err)
	require.Equal(t, "pending", initial.State)
	require.Equal(t, []string{"Build"}, initial.MissingWorkflows)
	require.Len(t, initial.Jobs, 1)
	r, err = server.InvokeTool(context.Background(), "watch_ci", map[string]any{"cursor": initial.Cursor})
	unchanged := readMonitorResult(t, r, err)
	require.Empty(t, unchanged.Workflows)
	require.Empty(t, unchanged.Jobs)
	require.False(t, unchanged.Complete)
	finished.Store(true)
	r, err = server.InvokeTool(context.Background(), "watch_ci", map[string]any{"cursor": unchanged.Cursor})
	changed := readMonitorResult(t, r, err)
	require.Len(t, changed.Workflows, 1)
	require.False(t, changed.Complete)
	require.Contains(t, changed.Removed, "job:2")
	require.Equal(t, int32(1), resolves.Load())
}

func TestMonitorExpectedAttemptAndSupersession(t *testing.T) {
	var attempt atomic.Int32
	attempt.Store(1)
	server := monitorTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		require.Equal(t, "/repos/o/r/actions/runs/1", r.URL.Path)
		fmt.Fprintf(w, `{"id":1,"workflow_id":10,"run_attempt":%d,"head_sha":%q,"name":"CI","status":"completed","conclusion":"success"}`, attempt.Load(), monitorTestSHA)
	}))
	r, err := server.InvokeTool(context.Background(), "verify_commit", map[string]any{"run_id": 1, "expected_attempt": 2})
	first := readMonitorResult(t, r, err)
	require.False(t, first.Complete)
	require.Equal(t, "awaiting_attempt", first.StopReason)
	require.Equal(t, 2, first.ExpectedAttempt)
	attempt.Store(2)
	r, err = server.InvokeTool(context.Background(), "watch_ci", map[string]any{"cursor": first.Cursor})
	done := readMonitorResult(t, r, err)
	require.True(t, done.Complete)
	require.Equal(t, "success", done.State)
	attempt.Store(3)
	r, err = server.InvokeTool(context.Background(), "watch_ci", map[string]any{"cursor": done.Cursor})
	superseded := readMonitorResult(t, r, err)
	require.Equal(t, "superseded", superseded.State)
}

func TestMonitorCursorValidationAndSelectorAmbiguity(t *testing.T) {
	server := monitorTestServer(t, http.NotFoundHandler())
	target := monitorTarget{Version: 1, Owner: "o", Repo: "r", SHA: monitorTestSHA, Expires: time.Now().Add(time.Hour).Unix()}
	cursor, err := server.encodeMonitorCursor(target)
	require.NoError(t, err)
	_, err = server.decodeMonitorCursor(cursor + "x")
	require.Error(t, err)
	target.Expires = time.Now().Add(-time.Hour).Unix()
	cursor, err = server.encodeMonitorCursor(target)
	require.NoError(t, err)
	_, err = server.decodeMonitorCursor(cursor)
	require.ErrorContains(t, err, "expired")
	_, _, err = server.newMonitorTarget(context.Background(), verifyInput{RunID: 1, Ref: "main"})
	require.ErrorContains(t, err, "cannot be combined")
}

func TestMonitorClientPoolRevalidatesAcrossCalls(t *testing.T) {
	var hits atomic.Int32
	server := monitorTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == `"v1"` {
			hits.Add(1)
			w.WriteHeader(304)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"total_count":1,"workflow_runs":[{"id":1,"workflow_id":10,"run_attempt":1,"head_sha":%q,"name":"CI","status":"completed","conclusion":"success"}]}`, monitorTestSHA)
	}))
	for range 2 {
		r, err := server.InvokeTool(context.Background(), "verify_commit", map[string]any{"ref": monitorTestSHA})
		out := readMonitorResult(t, r, err)
		require.True(t, out.Complete)
	}
	require.Equal(t, int32(1), hits.Load())
	a, _, _, err := server.clientFromInput(repoInput{})
	require.NoError(t, err)
	b, _, _, err := server.clientFromInput(repoInput{Repo: "other"})
	require.NoError(t, err)
	require.NotSame(t, a, b)
}

func TestMonitorExpectedNamesAreNotDisplayTruncated(t *testing.T) {
	name := strings.Repeat("n", 200)
	server := monitorTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"total_count":1,"workflow_runs":[{"id":1,"workflow_id":10,"run_attempt":1,"head_sha":%q,"name":%q,"status":"completed","conclusion":"success"}]}`, monitorTestSHA, name)
	}))
	r, err := server.InvokeTool(context.Background(), "verify_commit", map[string]any{"ref": monitorTestSHA, "expected_workflows": []string{name}})
	out := readMonitorResult(t, r, err)
	require.Empty(t, out.MissingWorkflows)
	require.True(t, out.Complete)
}

func TestMonitorBoundedWaitResumesMissingWorkflow(t *testing.T) {
	var registered atomic.Bool
	server := monitorTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !registered.Load() {
			fmt.Fprint(w, `{"total_count":0,"workflow_runs":[]}`)
			return
		}
		fmt.Fprintf(w, `{"total_count":1,"workflow_runs":[{"id":1,"workflow_id":10,"run_attempt":1,"head_sha":%q,"name":"CI","status":"completed","conclusion":"success"}]}`, monitorTestSHA)
	}))
	r, err := server.InvokeTool(context.Background(), "verify_commit", map[string]any{"ref": monitorTestSHA, "expected_workflows": []string{"CI"}, "wait_seconds": 1})
	pending := readMonitorResult(t, r, err)
	require.False(t, pending.Complete)
	require.NotEmpty(t, pending.Cursor)
	registered.Store(true)
	r, err = server.InvokeTool(context.Background(), "watch_ci", map[string]any{"cursor": pending.Cursor})
	done := readMonitorResult(t, r, err)
	require.True(t, done.Complete)
	require.Equal(t, "success", done.State)
}

func TestMonitorDiagnosesActiveFailure(t *testing.T) {
	server := monitorTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/o/r/actions/runs":
			fmt.Fprintf(w, `{"total_count":1,"workflow_runs":[{"id":1,"workflow_id":10,"run_attempt":1,"head_sha":%q,"name":"CI","status":"in_progress"}]}`, monitorTestSHA)
		case "/repos/o/r/actions/runs/1":
			fmt.Fprintf(w, `{"id":1,"workflow_id":10,"run_attempt":1,"head_sha":%q,"name":"CI","status":"in_progress"}`, monitorTestSHA)
		case "/repos/o/r/actions/runs/1/jobs", "/repos/o/r/actions/runs/1/attempts/1/jobs":
			fmt.Fprint(w, `{"total_count":1,"jobs":[{"id":2,"run_id":1,"run_attempt":1,"name":"test","status":"completed","conclusion":"failure","steps":[{"name":"test","number":1,"status":"completed","conclusion":"failure"}]}]}`)
		case "/repos/o/r/check-runs/2/annotations":
			fmt.Fprint(w, `[{"annotation_level":"failure","message":"test failed","path":"test.go","start_line":1}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	r, err := server.InvokeTool(context.Background(), "verify_commit", map[string]any{"ref": monitorTestSHA, "diagnose_on_failure": true})
	out := readMonitorResult(t, r, err)
	require.False(t, out.Complete)
	require.Len(t, out.Diagnoses, 1)
	require.Len(t, out.Diagnoses[0].FailedJobs, 1)
	r, err = server.InvokeTool(context.Background(), "watch_ci", map[string]any{"cursor": out.Cursor})
	unchanged := readMonitorResult(t, r, err)
	require.Empty(t, unchanged.Diagnoses)
	require.Equal(t, "unchanged", unchanged.StopReason)
}

func TestMonitorRejectsOversizedExpectedWorkflows(t *testing.T) {
	server := monitorTestServer(t, http.NotFoundHandler())
	_, _, err := server.newMonitorTarget(context.Background(), verifyInput{Ref: monitorTestSHA, ExpectedWorkflows: []string{strings.Repeat("界", 3000)}})
	require.ErrorContains(t, err, "aggregate limit")
}

func TestMonitorDeadlineIncludesTargetResolution(t *testing.T) {
	server := monitorTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	started := time.Now()
	r, err := server.InvokeTool(context.Background(), "verify_commit", map[string]any{"ref": "main", "wait_seconds": 1})
	require.Less(t, time.Since(started), 3*time.Second)
	require.True(t, err != nil || r.IsError)
}

func TestMonitorNeverDiagnosesAnOlderAttempt(t *testing.T) {
	server := monitorTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/repos/o/r/actions/runs/1", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":1,"workflow_id":10,"run_attempt":1,"head_sha":%q,"name":"CI","status":"completed","conclusion":"failure"}`, monitorTestSHA)
	}))
	r, err := server.InvokeTool(context.Background(), "verify_commit", map[string]any{"run_id": 1, "expected_attempt": 2, "diagnose_on_failure": true})
	out := readMonitorResult(t, r, err)
	require.Empty(t, out.Diagnoses)
	require.False(t, out.Complete)
	require.Equal(t, "awaiting_attempt", out.StopReason)
}
