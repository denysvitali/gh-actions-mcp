package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	githubapi "github.com/google/go-github/v89/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAnalyzeTiming(t *testing.T) {
	client, cleanup := newTimingAnalysisTestClient(t)
	defer cleanup()

	t.Run("workflow scope", func(t *testing.T) {
		analysis, err := client.AnalyzeTiming(context.Background(), &TimingAnalysisOptions{
			Workflow: "CI",
			Branch:   "main",
			Limit:    3,
		})
		require.NoError(t, err)

		assert.Equal(t, "workflow", analysis.Scope)
		assert.Equal(t, int64(50), analysis.WorkflowID)
		assert.Equal(t, "CI", analysis.WorkflowName)
		assert.Equal(t, "main", analysis.Branch)
		assert.Equal(t, 3, analysis.SampleCount)
		assert.InDelta(t, 500.0, analysis.Statistics.AverageSeconds, 0.01)
		assert.InDelta(t, 480.0, analysis.Statistics.MedianSeconds, 0.01)
		assert.InDelta(t, 420.0, analysis.Statistics.MinSeconds, 0.01)
		assert.InDelta(t, 600.0, analysis.Statistics.MaxSeconds, 0.01)

		require.NotNil(t, analysis.Focus)
		assert.Equal(t, int64(103), analysis.Focus.RunID)
		assert.InDelta(t, 600.0, analysis.Focus.DurationSeconds, 0.01)
		assert.InDelta(t, 150.0, analysis.Focus.DeltaFromAverageSeconds, 0.01)
		assert.InDelta(t, 33.333, analysis.Focus.DeltaFromAveragePercent, 0.01)
		assert.InDelta(t, 120.0, analysis.Focus.DeltaFromPreviousSeconds, 0.01)
		assert.InDelta(t, 25.0, analysis.Focus.DeltaFromPreviousPercent, 0.01)

		require.Len(t, analysis.RecentSamples, 3)
		assert.Equal(t, int64(103), analysis.RecentSamples[0].RunID)
		assert.Equal(t, int64(102), analysis.RecentSamples[1].RunID)
		assert.Equal(t, int64(101), analysis.RecentSamples[2].RunID)

		require.Len(t, analysis.JobBreakdown, 3)
		assert.Equal(t, "build", analysis.JobBreakdown[0].JobName)
		assert.InDelta(t, 75.0, analysis.JobBreakdown[0].DeltaFromAverageSeconds, 0.01)
		assert.Equal(t, "package", analysis.JobBreakdown[1].JobName)
		assert.Equal(t, "lint", analysis.JobBreakdown[2].JobName)

		require.NotEmpty(t, analysis.StepBreakdown)
		assert.Equal(t, "build", analysis.StepBreakdown[0].JobName)
		assert.Equal(t, "Unit Tests", analysis.StepBreakdown[0].StepName)
		assert.InDelta(t, 50.0, analysis.StepBreakdown[0].DeltaFromAverageSeconds, 0.01)
	})

	t.Run("step scope", func(t *testing.T) {
		analysis, err := client.AnalyzeTiming(context.Background(), &TimingAnalysisOptions{
			Workflow: "CI",
			Branch:   "main",
			JobName:  "build",
			StepName: "Unit Tests",
			Limit:    3,
		})
		require.NoError(t, err)

		assert.Equal(t, "step", analysis.Scope)
		assert.Equal(t, "build", analysis.JobName)
		assert.Equal(t, "Unit Tests", analysis.StepName)
		assert.Equal(t, 3, analysis.SampleCount)
		assert.InDelta(t, 176.666, analysis.Statistics.AverageSeconds, 0.01)
		assert.InDelta(t, 170.0, analysis.Statistics.MedianSeconds, 0.01)
		assert.InDelta(t, 150.0, analysis.Statistics.MinSeconds, 0.01)
		assert.InDelta(t, 210.0, analysis.Statistics.MaxSeconds, 0.01)
		require.NotNil(t, analysis.Focus)
		assert.Equal(t, int64(103), analysis.Focus.RunID)
		assert.InDelta(t, 50.0, analysis.Focus.DeltaFromAverageSeconds, 0.01)
		assert.InDelta(t, 31.25, analysis.Focus.DeltaFromAveragePercent, 0.01)
		assert.Nil(t, analysis.JobBreakdown)
		assert.Nil(t, analysis.StepBreakdown)
	})

	t.Run("step requires job", func(t *testing.T) {
		_, err := client.AnalyzeTiming(context.Background(), &TimingAnalysisOptions{
			Workflow: "CI",
			StepName: "Unit Tests",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "job_name is required")
	})
}

func newTimingAnalysisTestClient(t *testing.T) (*Client, func()) {
	t.Helper()

	const (
		owner = "test-owner"
		repo  = "test-repo"
	)

	mux := http.NewServeMux()

	mux.HandleFunc("/repos/"+owner+"/"+repo+"/actions/workflows", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"total_count": 1,
			"workflows": [
				{"id": 50, "name": "CI", "path": ".github/workflows/ci.yml", "state": "active"}
			]
		}`))
	})

	mux.HandleFunc("/repos/"+owner+"/"+repo+"/actions/workflows/50/runs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"total_count": 3,
			"workflow_runs": [
				{
					"id": 103, "name": "CI", "status": "completed", "conclusion": "success",
					"head_branch": "main", "head_sha": "sha103", "event": "push",
					"created_at": "2026-04-20T10:00:00Z", "updated_at": "2026-04-20T10:10:00Z",
					"run_started_at": "2026-04-20T10:00:00Z", "html_url": "https://example.com/run/103",
					"run_number": 13, "workflow_id": 50, "actor": {"login": "alice"}
				},
				{
					"id": 102, "name": "CI", "status": "completed", "conclusion": "success",
					"head_branch": "main", "head_sha": "sha102", "event": "push",
					"created_at": "2026-04-19T10:00:00Z", "updated_at": "2026-04-19T10:08:00Z",
					"run_started_at": "2026-04-19T10:00:00Z", "html_url": "https://example.com/run/102",
					"run_number": 12, "workflow_id": 50, "actor": {"login": "alice"}
				},
				{
					"id": 101, "name": "CI", "status": "completed", "conclusion": "success",
					"head_branch": "main", "head_sha": "sha101", "event": "push",
					"created_at": "2026-04-18T10:00:00Z", "updated_at": "2026-04-18T10:07:00Z",
					"run_started_at": "2026-04-18T10:00:00Z", "html_url": "https://example.com/run/101",
					"run_number": 11, "workflow_id": 50, "actor": {"login": "alice"}
				}
			]
		}`))
	})

	mux.HandleFunc("/repos/"+owner+"/"+repo+"/actions/runs/103/jobs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"total_count": 3,
			"jobs": [
				{
					"id": 203, "name": "build", "status": "completed", "conclusion": "success", "run_id": 103,
					"started_at": "2026-04-20T10:00:00Z", "completed_at": "2026-04-20T10:05:00Z",
					"steps": [
						{"name": "Checkout", "number": 1, "status": "completed", "conclusion": "success", "started_at": "2026-04-20T10:00:00Z", "completed_at": "2026-04-20T10:00:30Z"},
						{"name": "Setup Go", "number": 2, "status": "completed", "conclusion": "success", "started_at": "2026-04-20T10:00:30Z", "completed_at": "2026-04-20T10:01:30Z"},
						{"name": "Unit Tests", "number": 3, "status": "completed", "conclusion": "success", "started_at": "2026-04-20T10:01:30Z", "completed_at": "2026-04-20T10:05:00Z"}
					]
				},
				{
					"id": 204, "name": "lint", "status": "completed", "conclusion": "success", "run_id": 103,
					"started_at": "2026-04-20T10:00:00Z", "completed_at": "2026-04-20T10:01:40Z",
					"steps": [
						{"name": "Checkout", "number": 1, "status": "completed", "conclusion": "success", "started_at": "2026-04-20T10:00:00Z", "completed_at": "2026-04-20T10:00:25Z"},
						{"name": "golangci-lint", "number": 2, "status": "completed", "conclusion": "success", "started_at": "2026-04-20T10:00:25Z", "completed_at": "2026-04-20T10:01:40Z"}
					]
				},
				{
					"id": 205, "name": "package", "status": "completed", "conclusion": "success", "run_id": 103,
					"started_at": "2026-04-20T10:05:00Z", "completed_at": "2026-04-20T10:08:00Z",
					"steps": [
						{"name": "Build Binary", "number": 1, "status": "completed", "conclusion": "success", "started_at": "2026-04-20T10:05:00Z", "completed_at": "2026-04-20T10:08:00Z"}
					]
				}
			]
		}`))
	})

	mux.HandleFunc("/repos/"+owner+"/"+repo+"/actions/runs/102/jobs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"total_count": 3,
			"jobs": [
				{
					"id": 202, "name": "build", "status": "completed", "conclusion": "success", "run_id": 102,
					"started_at": "2026-04-19T10:00:00Z", "completed_at": "2026-04-19T10:04:00Z",
					"steps": [
						{"name": "Checkout", "number": 1, "status": "completed", "conclusion": "success", "started_at": "2026-04-19T10:00:00Z", "completed_at": "2026-04-19T10:00:20Z"},
						{"name": "Setup Go", "number": 2, "status": "completed", "conclusion": "success", "started_at": "2026-04-19T10:00:20Z", "completed_at": "2026-04-19T10:01:10Z"},
						{"name": "Unit Tests", "number": 3, "status": "completed", "conclusion": "success", "started_at": "2026-04-19T10:01:10Z", "completed_at": "2026-04-19T10:04:00Z"}
					]
				},
				{
					"id": 212, "name": "lint", "status": "completed", "conclusion": "success", "run_id": 102,
					"started_at": "2026-04-19T10:00:00Z", "completed_at": "2026-04-19T10:01:30Z",
					"steps": [
						{"name": "Checkout", "number": 1, "status": "completed", "conclusion": "success", "started_at": "2026-04-19T10:00:00Z", "completed_at": "2026-04-19T10:00:20Z"},
						{"name": "golangci-lint", "number": 2, "status": "completed", "conclusion": "success", "started_at": "2026-04-19T10:00:20Z", "completed_at": "2026-04-19T10:01:30Z"}
					]
				},
				{
					"id": 222, "name": "package", "status": "completed", "conclusion": "success", "run_id": 102,
					"started_at": "2026-04-19T10:04:00Z", "completed_at": "2026-04-19T10:06:30Z",
					"steps": [
						{"name": "Build Binary", "number": 1, "status": "completed", "conclusion": "success", "started_at": "2026-04-19T10:04:00Z", "completed_at": "2026-04-19T10:06:30Z"}
					]
				}
			]
		}`))
	})

	mux.HandleFunc("/repos/"+owner+"/"+repo+"/actions/runs/101/jobs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"total_count": 3,
			"jobs": [
				{
					"id": 201, "name": "build", "status": "completed", "conclusion": "success", "run_id": 101,
					"started_at": "2026-04-18T10:00:00Z", "completed_at": "2026-04-18T10:03:30Z",
					"steps": [
						{"name": "Checkout", "number": 1, "status": "completed", "conclusion": "success", "started_at": "2026-04-18T10:00:00Z", "completed_at": "2026-04-18T10:00:20Z"},
						{"name": "Setup Go", "number": 2, "status": "completed", "conclusion": "success", "started_at": "2026-04-18T10:00:20Z", "completed_at": "2026-04-18T10:01:00Z"},
						{"name": "Unit Tests", "number": 3, "status": "completed", "conclusion": "success", "started_at": "2026-04-18T10:01:00Z", "completed_at": "2026-04-18T10:03:30Z"}
					]
				},
				{
					"id": 211, "name": "lint", "status": "completed", "conclusion": "success", "run_id": 101,
					"started_at": "2026-04-18T10:00:00Z", "completed_at": "2026-04-18T10:01:20Z",
					"steps": [
						{"name": "Checkout", "number": 1, "status": "completed", "conclusion": "success", "started_at": "2026-04-18T10:00:00Z", "completed_at": "2026-04-18T10:00:20Z"},
						{"name": "golangci-lint", "number": 2, "status": "completed", "conclusion": "success", "started_at": "2026-04-18T10:00:20Z", "completed_at": "2026-04-18T10:01:20Z"}
					]
				},
				{
					"id": 221, "name": "package", "status": "completed", "conclusion": "success", "run_id": 101,
					"started_at": "2026-04-18T10:03:30Z", "completed_at": "2026-04-18T10:05:40Z",
					"steps": [
						{"name": "Build Binary", "number": 1, "status": "completed", "conclusion": "success", "started_at": "2026-04-18T10:03:30Z", "completed_at": "2026-04-18T10:05:40Z"}
					]
				}
			]
		}`))
	})

	ts := httptest.NewServer(mux)

	baseURL := ts.URL + "/"
	ghc, err := githubapi.NewClient(githubapi.WithHTTPClient(ts.Client()), githubapi.WithAuthToken("test-token"), githubapi.WithURLs(&baseURL, nil))
	require.NoError(t, err)

	client := &Client{
		owner:        owner,
		repo:         repo,
		gh:           ghc,
		perPageLimit: 50,
	}

	return client, ts.Close
}
