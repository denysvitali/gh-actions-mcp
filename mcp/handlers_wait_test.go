package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/denysvitali/gh-actions-mcp/config"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLegacyWaitPassesExpectedAttempt(t *testing.T) {
	for _, allJobs := range []bool{false, true} {
		name := "fail_fast"
		if allJobs {
			name = "all_jobs"
		}
		t.Run(name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("/repos/owner/repo/actions/runs/1", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":1,"run_attempt":3,"status":"completed","conclusion":"success"}`))
			})
			server := newWaitTestServer(t, mux)
			target := waitTargetRun
			if allJobs {
				target = waitTargetAllJobs
			}
			_, result, err := server.waitRunTyped(context.Background(), nil, waitRunInput{RunID: 1, ExpectedAttempt: 2}, target)
			require.NoError(t, err)
			assert.Equal(t, "superseded", result.Status)
			assert.Equal(t, 3, result.RunAttempt)
			assert.Empty(t, result.Conclusion)
		})
	}
}

func TestLegacyWaitReportsFailureWithoutInventingCompletion(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/repo/actions/runs/1", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":1,"run_attempt":2,"status":"in_progress","updated_at":"2026-10-04T08:00:00Z"}`))
	})
	mux.HandleFunc("/repos/owner/repo/actions/runs/1/attempts/2/jobs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"total_count":1,"jobs":[{"id":10,"run_attempt":2,"status":"completed","conclusion":"failure"}]}`))
	})
	server := newWaitTestServer(t, mux)
	_, result, err := server.waitForRunTyped(context.Background(), nil, waitRunInput{RunID: 1, ExpectedAttempt: 2})
	require.NoError(t, err)
	assert.Equal(t, "failure_observed", result.Status)
	assert.Equal(t, "in_progress", result.RunStatus)
	assert.Equal(t, 2, result.RunAttempt)
	assert.Empty(t, result.CompletedAt)
}

func TestLegacyCommitWaitResolvesBranchToExactSHA(t *testing.T) {
	const sha = "0123456789abcdef0123456789abcdef01234567"
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/repo/commits/main", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sha":"` + sha + `"}`))
	})
	mux.HandleFunc("/repos/owner/repo/actions/runs", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, sha, r.URL.Query().Get("head_sha"))
		assert.Empty(t, r.URL.Query().Get("branch"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"total_count":1,"workflow_runs":[{"id":1,"workflow_id":9,"run_attempt":2,"head_sha":"` + sha + `","status":"completed","conclusion":"success"}]}`))
	})
	server := newWaitTestServer(t, mux)
	_, result, err := server.waitForCommitChecksTyped(context.Background(), nil, waitChecksInput{Ref: "main"})
	require.NoError(t, err)
	assert.Equal(t, "success", result.OverallConclusion)
	assert.Equal(t, sha, result.SHA)
}

func newWaitTestServer(t *testing.T, mux *http.ServeMux) *MCPServer {
	t.Helper()
	api := httptest.NewServer(mux)
	t.Cleanup(api.Close)
	logger := logrus.New()
	logger.SetOutput(discardWriter{})
	server, err := NewMCPServer(&config.Config{Token: "test-token", RepoOwner: "owner", RepoName: "repo", APIBaseURL: api.URL + "/"}, logger)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, server.Close()) })
	return server
}
