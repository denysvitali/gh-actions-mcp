package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	githubapi "github.com/google/go-github/v89/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestErrorPatterns(t *testing.T) {
	tests := []struct {
		line    string
		matches bool
	}{
		{"error: compilation failed", true},
		{"ERROR: something went wrong", true},
		{"error[E0308]: mismatched types", true},
		{"FAIL: TestSomething (0.00s)", true},
		{"fatal: not a git repository", true},
		{"panic: runtime error: index out of range", true},
		{"Traceback (most recent call last):", true},
		{"E   AssertionError: 1 != 2", true},
		{"--- FAIL: TestFoo (0.01s)", true},
		{"exit code 1", true},
		{"command 'make build' failed", true},
		{"Process completed with exit code 1.", true},
		{"##[error]Process completed with exit code 2.", true},
		{"everything is fine", false},
		{"running tests...", false},
		{"Build succeeded", false},
		{"warning: unused variable", false},
	}

	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			matched := false
			for _, pattern := range errorPatterns {
				if pattern.MatchString(tt.line) {
					matched = true
					break
				}
			}
			assert.Equal(t, tt.matches, matched, "line: %q", tt.line)
		})
	}
}

func TestGetCheckRunAnnotationErrors(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/repo/check-runs/200/annotations", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "100", r.URL.Query().Get("per_page"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"path":"internal/build.go","start_line":42,"annotation_level":"failure","title":"Compile","message":"undefined: Widget"},
			{"path":"internal/build.go","start_line":10,"annotation_level":"warning","message":"unused import"}
		]`))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	baseURL := ts.URL + "/"
	ghc, err := githubapi.NewClient(githubapi.WithHTTPClient(ts.Client()), githubapi.WithAuthToken("test-token"), githubapi.WithURLs(&baseURL, nil))
	require.NoError(t, err)
	client := &Client{owner: "owner", repo: "repo", gh: ghc}

	assert.Equal(t, []string{"internal/build.go:42: Compile: undefined: Widget"}, client.getCheckRunAnnotationErrors(context.Background(), 200, 150))
}

func TestBuildDiagnosisSummary(t *testing.T) {
	client := NewClient("token", "owner", "repo")

	t.Run("no failed jobs", func(t *testing.T) {
		d := &FailureDiagnosis{
			RunID:      123,
			Conclusion: "cancelled",
			FailedJobs: nil,
		}
		summary := client.buildDiagnosisSummary(d)
		assert.Contains(t, summary, "no failed jobs found")
	})

	t.Run("with failed jobs", func(t *testing.T) {
		d := &FailureDiagnosis{
			RunID:      123,
			Conclusion: "failure",
			FailedJobs: []*FailedJob{
				{
					JobName:    "Build",
					Conclusion: "failure",
					ErrorLines: []string{"error: foo", "error: bar"},
				},
				{
					JobName:    "Test",
					Conclusion: "failure",
					ErrorLines: []string{"FAIL: TestBaz"},
				},
			},
		}
		summary := client.buildDiagnosisSummary(d)
		assert.Contains(t, summary, "2 failed job(s)")
		assert.Contains(t, summary, "Build, Test")
		assert.Contains(t, summary, "3 error line(s)")
	})

	t.Run("with flakiness info", func(t *testing.T) {
		d := &FailureDiagnosis{
			RunID:      123,
			Conclusion: "failure",
			FailedJobs: []*FailedJob{
				{JobName: "CI", Conclusion: "failure", ErrorLines: []string{"err"}},
			},
			Flakiness: &FlakinessInfo{
				RecentRuns:       10,
				RecentFailures:   3,
				RecentSuccesses:  7,
				SameFailureCount: 3,
				Verdict:          "likely_flake",
			},
		}
		summary := client.buildDiagnosisSummary(d)
		assert.Contains(t, summary, "likely_flake")
		assert.Contains(t, summary, "7 succeeded")
	})
}

func TestFailureDiagnosisJSON(t *testing.T) {
	d := &FailureDiagnosis{
		RunID:      42,
		RunName:    "CI",
		Conclusion: "failure",
		FailedJobs: []*FailedJob{
			{
				JobID:      100,
				JobName:    "build",
				Conclusion: "failure",
				FailedSteps: []*FailedStep{
					{Name: "Run tests", Number: 3, Conclusion: "failure"},
				},
				ErrorLines: []string{"--- FAIL: TestFoo"},
			},
		},
		Flakiness: &FlakinessInfo{
			RecentRuns:      5,
			RecentFailures:  1,
			RecentSuccesses: 4,
			Verdict:         "first_failure",
		},
		Summary: "1 failed job(s): build.",
	}

	data, err := json.Marshal(d)
	require.NoError(t, err)

	var decoded FailureDiagnosis
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, int64(42), decoded.RunID)
	assert.Equal(t, "CI", decoded.RunName)
	assert.Len(t, decoded.FailedJobs, 1)
	assert.Equal(t, "build", decoded.FailedJobs[0].JobName)
	assert.Len(t, decoded.FailedJobs[0].FailedSteps, 1)
	assert.Equal(t, "Run tests", decoded.FailedJobs[0].FailedSteps[0].Name)
	assert.Len(t, decoded.FailedJobs[0].ErrorLines, 1)
	assert.NotNil(t, decoded.Flakiness)
	assert.Equal(t, "first_failure", decoded.Flakiness.Verdict)
}

func TestDiagnoseFailure_Integration(t *testing.T) {
	const (
		owner = "test-owner"
		repo  = "test-repo"
	)

	mux := http.NewServeMux()
	redirectBase := ""

	// GET /repos/{owner}/{repo}/actions/runs/100
	mux.HandleFunc("/repos/"+owner+"/"+repo+"/actions/runs/100", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": 100, "name": "CI", "status": "completed", "conclusion": "failure",
			"head_branch": "main", "head_sha": "abc123", "html_url": "https://example.com/run/100",
			"event": "push", "run_number": 10, "workflow_id": 50
		}`))
	})

	// GET /repos/{owner}/{repo}/actions/runs/100/jobs
	mux.HandleFunc("/repos/"+owner+"/"+repo+"/actions/runs/100/jobs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"total_count": 2,
			"jobs": [
				{
					"id": 200, "name": "build", "status": "completed", "conclusion": "failure",
					"run_id": 100,
					"steps": [
						{"name": "Checkout", "number": 1, "status": "completed", "conclusion": "success"},
						{"name": "Build", "number": 2, "status": "completed", "conclusion": "failure"}
					]
				},
				{
					"id": 201, "name": "lint", "status": "completed", "conclusion": "success",
					"run_id": 100,
					"steps": [
						{"name": "Lint", "number": 1, "status": "completed", "conclusion": "success"}
					]
				}
			]
		}`))
	})

	// GET /repos/{owner}/{repo}/actions/jobs/200/logs
	mux.HandleFunc("/repos/"+owner+"/"+repo+"/actions/jobs/200/logs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", redirectBase+"/blob/job200.log")
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/blob/job200.log", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("2024-01-15T10:30:00.1234567Z Starting build\n2024-01-15T10:30:01.1234567Z error: cannot find module\n2024-01-15T10:30:02.1234567Z ##[error]Process completed with exit code 1.\n"))
	})

	// GET /repos/{owner}/{repo}/actions/workflows/50/runs (for flakiness check)
	mux.HandleFunc("/repos/"+owner+"/"+repo+"/actions/workflows/50/runs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"total_count": 3,
			"workflow_runs": [
				{"id": 100, "name": "CI", "status": "completed", "conclusion": "failure", "head_branch": "main", "run_number": 10, "html_url": "u", "workflow_id": 50},
				{"id": 99, "name": "CI", "status": "completed", "conclusion": "success", "head_branch": "main", "run_number": 9, "html_url": "u", "workflow_id": 50},
				{"id": 98, "name": "CI", "status": "completed", "conclusion": "success", "head_branch": "main", "run_number": 8, "html_url": "u", "workflow_id": 50}
			]
		}`))
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()
	redirectBase = ts.URL

	baseURL := ts.URL + "/"
	ghc, err := githubapi.NewClient(githubapi.WithHTTPClient(ts.Client()), githubapi.WithAuthToken("test-token"), githubapi.WithURLs(&baseURL, nil))
	require.NoError(t, err)

	client := &Client{
		owner:        owner,
		repo:         repo,
		gh:           ghc,
		perPageLimit: 50,
	}

	diagnosis, err := client.DiagnoseFailure(context.Background(), 100, true, 50)
	require.NoError(t, err)

	assert.Equal(t, int64(100), diagnosis.RunID)
	assert.Equal(t, "CI", diagnosis.RunName)
	assert.Equal(t, "failure", diagnosis.Conclusion)

	// Should have 1 failed job (build), lint succeeded
	require.Len(t, diagnosis.FailedJobs, 1)
	assert.Equal(t, "build", diagnosis.FailedJobs[0].JobName)
	assert.Equal(t, int64(200), diagnosis.FailedJobs[0].JobID)

	// Should have identified the failed step
	require.Len(t, diagnosis.FailedJobs[0].FailedSteps, 1)
	assert.Equal(t, "Build", diagnosis.FailedJobs[0].FailedSteps[0].Name)

	// Should have extracted error lines (timestamps stripped)
	require.GreaterOrEqual(t, len(diagnosis.FailedJobs[0].ErrorLines), 1)
	foundModuleError := false
	for _, line := range diagnosis.FailedJobs[0].ErrorLines {
		if strings.Contains(line, "cannot find module") {
			foundModuleError = true
		}
		// Timestamp should be stripped
		assert.NotContains(t, line, "2024-01-15T10:30")
	}
	assert.True(t, foundModuleError, "should have found 'cannot find module' error")

	// Flakiness check
	require.NotNil(t, diagnosis.Flakiness)
	assert.Equal(t, "first_failure", diagnosis.Flakiness.Verdict)
	assert.Equal(t, 2, diagnosis.Flakiness.RecentRuns)
	assert.Equal(t, 2, diagnosis.Flakiness.RecentSuccesses)

	// Summary should mention the failed job
	assert.Contains(t, diagnosis.Summary, "build")
	assert.Contains(t, diagnosis.Summary, "1 failed job")
}

func TestDiagnoseFailure_SuccessfulRun(t *testing.T) {
	const (
		owner = "test-owner"
		repo  = "test-repo"
	)

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/"+owner+"/"+repo+"/actions/runs/100", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": 100, "name": "CI", "status": "completed", "conclusion": "success",
			"head_branch": "main", "html_url": "https://example.com/run/100",
			"run_number": 10, "workflow_id": 50
		}`))
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	baseURL := ts.URL + "/"
	ghc, err := githubapi.NewClient(githubapi.WithHTTPClient(ts.Client()), githubapi.WithAuthToken("test-token"), githubapi.WithURLs(&baseURL, nil))
	require.NoError(t, err)

	client := &Client{owner: owner, repo: repo, gh: ghc, perPageLimit: 50}

	diagnosis, err := client.DiagnoseFailure(context.Background(), 100, false, 50)
	require.NoError(t, err)
	assert.Contains(t, diagnosis.Summary, "succeeded")
	assert.Nil(t, diagnosis.FailedJobs)
}

func TestDiagnoseFailure_InProgressRun(t *testing.T) {
	const (
		owner = "test-owner"
		repo  = "test-repo"
	)

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/"+owner+"/"+repo+"/actions/runs/100", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": 100, "name": "CI", "status": "in_progress", "conclusion": "",
			"head_branch": "main", "html_url": "https://example.com/run/100",
			"run_number": 10, "workflow_id": 50
		}`))
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	baseURL := ts.URL + "/"
	ghc, err := githubapi.NewClient(githubapi.WithHTTPClient(ts.Client()), githubapi.WithAuthToken("test-token"), githubapi.WithURLs(&baseURL, nil))
	require.NoError(t, err)

	client := &Client{owner: owner, repo: repo, gh: ghc, perPageLimit: 50}

	diagnosis, err := client.DiagnoseFailure(context.Background(), 100, false, 50)
	require.NoError(t, err)
	assert.Contains(t, diagnosis.Summary, "still in_progress")
}

func TestExtractErrorLines(t *testing.T) {
	t.Parallel()

	t.Run("dedupes, strips timestamps and honours maxLines", func(t *testing.T) {
		t.Parallel()

		logs := strings.Join([]string{
			"2024-01-15T10:30:00.1234567Z ##[error]first failure",
			"2024-01-15T10:30:01.1234567Z ##[error]first failure", // duplicate after stripping
			"   ",                 // blank lines are skipped
			"plain progress line", // no pattern match
			"--- FAIL: TestSomething",
			"panic: runtime error",
		}, "\n")

		base := ""
		mux := http.NewServeMux()
		mux.HandleFunc("/repos/owner/repo/actions/jobs/10/logs", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", base+"/blob/job.log")
			w.WriteHeader(http.StatusFound)
		})
		mux.HandleFunc("/blob/job.log", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(logs))
		})
		client, url := newMuxClientWithURL(t, mux)
		base = url

		lines := client.extractErrorLines(context.Background(), 0, 10, 10)
		assert.Equal(t, []string{
			"##[error]first failure",
			"--- FAIL: TestSomething",
			"panic: runtime error",
		}, lines)

		capped := client.extractErrorLines(context.Background(), 0, 10, 1)
		assert.Len(t, capped, 1)
	})

	t.Run("without a run ID a log failure is reported inline", func(t *testing.T) {
		t.Parallel()

		mux := http.NewServeMux()
		mux.HandleFunc("/repos/owner/repo/actions/jobs/10/logs", statusHandler(http.StatusNotFound))
		client := newMuxClient(t, mux)

		lines := client.extractErrorLines(context.Background(), 0, 10, 10)
		require.Len(t, lines, 1)
		assert.Contains(t, lines[0], "[could not fetch logs:")
		assert.NotContains(t, lines[0], "archive fallback")
	})

	t.Run("with a run ID both failures are reported inline", func(t *testing.T) {
		t.Parallel()

		mux := http.NewServeMux()
		mux.HandleFunc("/repos/owner/repo/actions/jobs/10/logs", statusHandler(http.StatusNotFound))
		mux.HandleFunc("/repos/owner/repo/actions/runs/5/jobs", statusHandler(http.StatusNotFound))
		client := newMuxClient(t, mux)

		lines := client.extractErrorLines(context.Background(), 5, 10, 10)
		require.Len(t, lines, 1)
		assert.Contains(t, lines[0], "archive fallback failed")
	})

	t.Run("falls back to the run archive when the job log endpoint 404s", func(t *testing.T) {
		t.Parallel()

		zipData := makeArtifactZIP(t, map[string]string{"Build/1_step.txt": "##[error]archive failure"})

		base := ""
		mux := http.NewServeMux()
		mux.HandleFunc("/repos/owner/repo/actions/jobs/10/logs", statusHandler(http.StatusNotFound))
		mux.HandleFunc("/repos/owner/repo/actions/runs/5/jobs", jsonHandler(`{"total_count":1,"jobs":[{"id":10,"name":"Build","status":"completed","conclusion":"failure"}]}`))
		mux.HandleFunc("/repos/owner/repo/actions/runs/5/logs", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", base+"/blob/run.zip")
			w.WriteHeader(http.StatusFound)
		})
		mux.HandleFunc("/blob/run.zip", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/zip")
			_, _ = w.Write(zipData)
		})
		client, url := newMuxClientWithURL(t, mux)
		base = url

		lines := client.extractErrorLines(context.Background(), 5, 10, 10)
		assert.Equal(t, []string{"##[error]archive failure"}, lines)
	})
}

func TestCheckFlakiness(t *testing.T) {
	t.Parallel()

	// runsJSON always contains the focus run (id 1) plus history. The focus run is
	// skipped, as is any run that is not completed.
	newClient := func(t *testing.T, runsJSON string, jobsByRun map[string]string) *Client {
		t.Helper()

		mux := http.NewServeMux()
		mux.HandleFunc("/repos/owner/repo/actions/workflows/50/runs", jsonHandler(runsJSON))
		for path, body := range jobsByRun {
			mux.HandleFunc(path, jsonHandler(body))
		}
		return newMuxClient(t, mux)
	}

	focus := &WorkflowRun{ID: 1, WorkflowID: 50, Branch: "main"}
	failedJobs := []*FailedJob{{JobName: "build"}}
	buildFailed := `{"total_count":1,"jobs":[{"id":9,"name":"build","status":"completed","conclusion":"failure"}]}`

	t.Run("no history is unknown", func(t *testing.T) {
		t.Parallel()

		client := newClient(t, `{"total_count":1,"workflow_runs":[{"id":1,"status":"completed","conclusion":"failure","workflow_id":50}]}`, nil)
		info := client.checkFlakiness(context.Background(), focus, failedJobs)
		assert.Equal(t, "unknown", info.Verdict)
		assert.Equal(t, 0, info.RecentRuns)
	})

	t.Run("same job failing twice alongside a success is a likely flake", func(t *testing.T) {
		t.Parallel()

		client := newClient(t, `{"total_count":4,"workflow_runs":[
			{"id":1,"status":"completed","conclusion":"failure","workflow_id":50},
			{"id":2,"status":"completed","conclusion":"failure","workflow_id":50},
			{"id":3,"status":"completed","conclusion":"failure","workflow_id":50},
			{"id":4,"status":"completed","conclusion":"success","workflow_id":50}
		]}`, map[string]string{
			"/repos/owner/repo/actions/runs/2/jobs": buildFailed,
			"/repos/owner/repo/actions/runs/3/jobs": buildFailed,
		})

		info := client.checkFlakiness(context.Background(), focus, failedJobs)
		assert.Equal(t, "likely_flake", info.Verdict)
		assert.Equal(t, 3, info.RecentRuns)
		assert.Equal(t, 2, info.RecentFailures)
		assert.Equal(t, 1, info.RecentSuccesses)
		assert.Equal(t, 2, info.SameFailureCount)
	})

	t.Run("failures with no successes is a likely regression", func(t *testing.T) {
		t.Parallel()

		client := newClient(t, `{"total_count":2,"workflow_runs":[
			{"id":1,"status":"completed","conclusion":"failure","workflow_id":50},
			{"id":2,"status":"completed","conclusion":"failure","workflow_id":50}
		]}`, map[string]string{"/repos/owner/repo/actions/runs/2/jobs": buildFailed})

		info := client.checkFlakiness(context.Background(), focus, failedJobs)
		assert.Equal(t, "likely_regression", info.Verdict)
	})

	t.Run("only successes in history is a first failure", func(t *testing.T) {
		t.Parallel()

		client := newClient(t, `{"total_count":2,"workflow_runs":[
			{"id":1,"status":"completed","conclusion":"failure","workflow_id":50},
			{"id":2,"status":"completed","conclusion":"success","workflow_id":50}
		]}`, nil)

		info := client.checkFlakiness(context.Background(), focus, failedJobs)
		assert.Equal(t, "first_failure", info.Verdict)
		assert.Equal(t, 1, info.RecentSuccesses)
	})

	t.Run("in-progress runs are not counted", func(t *testing.T) {
		t.Parallel()

		client := newClient(t, `{"total_count":3,"workflow_runs":[
			{"id":1,"status":"completed","conclusion":"failure","workflow_id":50},
			{"id":2,"status":"in_progress","workflow_id":50},
			{"id":3,"status":"queued","workflow_id":50}
		]}`, nil)

		info := client.checkFlakiness(context.Background(), focus, failedJobs)
		assert.Equal(t, "unknown", info.Verdict)
		assert.Equal(t, 0, info.RecentRuns)
	})

	t.Run("a run history lookup failure is unknown", func(t *testing.T) {
		t.Parallel()

		mux := http.NewServeMux()
		mux.HandleFunc("/repos/owner/repo/actions/workflows/50/runs", statusHandler(http.StatusForbidden))
		client := newMuxClient(t, mux)

		info := client.checkFlakiness(context.Background(), focus, failedJobs)
		assert.Equal(t, "unknown", info.Verdict)
	})

	t.Run("a different failing job does not count as the same failure", func(t *testing.T) {
		t.Parallel()

		client := newClient(t, `{"total_count":3,"workflow_runs":[
			{"id":1,"status":"completed","conclusion":"failure","workflow_id":50},
			{"id":2,"status":"completed","conclusion":"failure","workflow_id":50},
			{"id":3,"status":"completed","conclusion":"success","workflow_id":50}
		]}`, map[string]string{
			"/repos/owner/repo/actions/runs/2/jobs": `{"total_count":1,"jobs":[{"id":9,"name":"lint","status":"completed","conclusion":"failure"}]}`,
		})

		info := client.checkFlakiness(context.Background(), focus, failedJobs)
		assert.Equal(t, 0, info.SameFailureCount)
		assert.Equal(t, "likely_regression", info.Verdict)
	})
}

func TestDiagnoseFailure_PrefersCheckAnnotations(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/repo/actions/runs/1", jsonHandler(`{"id":1,"name":"CI","status":"completed","conclusion":"failure","head_branch":"main","head_sha":"deadbeef","html_url":"https://example.com/1","workflow_id":50}`))
	mux.HandleFunc("/repos/owner/repo/actions/runs/1/jobs", jsonHandler(`{"total_count":2,"jobs":[
		{"id":10,"name":"build","status":"completed","conclusion":"failure","steps":[
			{"name":"Compile","number":3,"status":"completed","conclusion":"failure"},
			{"name":"Upload","number":4,"status":"completed","conclusion":"success"}
		]},
		{"id":11,"name":"ok","status":"completed","conclusion":"success"}
	]}`))
	mux.HandleFunc("/repos/owner/repo/check-runs/10/annotations", jsonHandler(`[
		{"path":"main.go","start_line":12,"annotation_level":"failure","title":"vet","message":"undefined: foo"},
		{"path":"main.go","start_line":12,"annotation_level":"failure","title":"vet","message":"undefined: foo"},
		{"path":"main.go","annotation_level":"notice","message":"ignored"},
		{"path":"other.go","annotation_level":"failure","message":"   "}
	]`))
	client := newMuxClient(t, mux)

	diagnosis, err := client.DiagnoseFailure(context.Background(), 1, false, 50)
	require.NoError(t, err)

	require.Len(t, diagnosis.FailedJobs, 1, "only failed jobs are reported")
	job := diagnosis.FailedJobs[0]
	assert.Equal(t, "build", job.JobName)
	assert.Equal(t, "check_annotations", job.ErrorSource)
	assert.Equal(t, []string{"main.go:12: vet: undefined: foo"}, job.ErrorLines)
	require.Len(t, job.FailedSteps, 1)
	assert.Equal(t, "Compile", job.FailedSteps[0].Name)
	assert.Nil(t, diagnosis.Flakiness, "flakiness is opt-in")
	assert.Contains(t, diagnosis.Summary, "1 failed job(s): build")
}

func TestDiagnoseFailure_RunLookupFailure(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/repo/actions/runs/1", statusHandler(http.StatusNotFound))
	client := newMuxClient(t, mux)

	_, err := client.DiagnoseFailure(context.Background(), 1, false, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get run 1")
}

func TestGetCheckRunAnnotationErrors_NoAnnotations(t *testing.T) {
	t.Parallel()

	t.Run("maxLines zero short-circuits without an API call", func(t *testing.T) {
		t.Parallel()

		client := newMuxClient(t, http.NewServeMux())
		assert.Nil(t, client.getCheckRunAnnotationErrors(context.Background(), 10, 0))
	})

	t.Run("an API failure yields nil rather than an error", func(t *testing.T) {
		t.Parallel()

		mux := http.NewServeMux()
		mux.HandleFunc("/repos/owner/repo/check-runs/10/annotations", statusHandler(http.StatusForbidden))
		client := newMuxClient(t, mux)
		assert.Empty(t, client.getCheckRunAnnotationErrors(context.Background(), 10, 50))
	})
}
