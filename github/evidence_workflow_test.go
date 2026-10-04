package github

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDiagnosticEvidenceKeepsVulnerabilityAndFix(t *testing.T) {
	logs := "Vulnerability #1: GO-2026-1234\nFound in: example.org/lib@v1.0\nFixed in: example.org/lib@v1.2\n##[error]Process completed with exit code 1.\n"
	lines, evidence := mergeDiagnosticEvidence(99, []string{"Process completed with exit code 1."}, logs, 20)
	require.Contains(t, strings.Join(lines, "\n"), "GO-2026-1234")
	require.Contains(t, strings.Join(lines, "\n"), "Fixed in: example.org/lib@v1.2")
	require.Equal(t, 1, evidence[0].Line)
	require.Equal(t, int64(99), evidence[0].JobID)
	lines, _ = mergeDiagnosticEvidence(99, []string{strings.Repeat("x", 100000)}, "", 20)
	require.LessOrEqual(t, len(strings.Join(lines, "")), maxDiagnosticBytes)
}

func TestFlakinessCountsTenAndSameSHA(t *testing.T) {
	mux := http.NewServeMux()
	runs := []string{}
	for i := 2; i < 14; i++ {
		runs = append(runs, fmt.Sprintf(`{"id":%d,"status":"completed","conclusion":"success","head_sha":"abc","workflow_id":50}`, i))
	}
	mux.HandleFunc("/repos/owner/repo/actions/workflows/50/runs", jsonHandler(`{"workflow_runs":[`+strings.Join(runs, ",")+`]}`))
	info := newMuxClient(t, mux).checkFlakiness(context.Background(), &WorkflowRun{ID: 1, WorkflowID: 50, HeadSHA: "abc"}, nil)
	require.Equal(t, 10, info.RecentRuns)
	require.Equal(t, 10, info.RecentSuccesses)
	require.Equal(t, 10, info.SameSHASuccesses)
	require.Equal(t, "likely_flake", info.Verdict)
}

func TestLogArchiveCacheAttemptAndManifest(t *testing.T) {
	mux := http.NewServeMux()
	var downloads atomic.Int32
	var attempt atomic.Int32
	attempt.Store(1)
	mux.HandleFunc("/repos/owner/repo/actions/runs/1", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"id":1,"status":"completed","run_attempt":%d}`, attempt.Load())
	})
	data := makeArtifactZIP(t, map[string]string{"job/1_step.txt": "error: detailed cause\n"})
	for _, number := range []int{1, 2} {
		mux.HandleFunc(fmt.Sprintf("/repos/owner/repo/actions/runs/1/attempts/%d/logs", number), func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", "http://"+r.Host+"/blob")
			w.WriteHeader(http.StatusFound)
		})
	}
	mux.HandleFunc("/repos/owner/repo/actions/runs/1/logs", func(w http.ResponseWriter, r *http.Request) {
		t.Error("known attempts must not fetch the mutable latest archive")
		w.WriteHeader(http.StatusConflict)
	})

	mux.HandleFunc("/blob", func(w http.ResponseWriter, r *http.Request) { downloads.Add(1); _, _ = w.Write(data) })
	client := newMuxClient(t, mux)
	files, err := client.GetWorkflowLogFiles(context.Background(), 1)
	require.NoError(t, err)
	require.Len(t, files, 1)
	logs, err := client.GetWorkflowLogs(context.Background(), 1, 0, 0, 0, true, nil)
	require.NoError(t, err)
	require.Contains(t, logs, "detailed cause")
	require.Equal(t, int32(1), downloads.Load())
	attempt.Store(2)
	_, err = client.GetWorkflowLogs(context.Background(), 1, 0, 0, 0, true, nil)
	require.NoError(t, err)
	require.Equal(t, int32(2), downloads.Load())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = client.GetWorkflowLogFiles(ctx, 1)
	require.ErrorIs(t, err, context.Canceled)
}

func TestArtifactManifestPaginationAndSummary(t *testing.T) {
	data := makeArtifactZIP(t, map[string]string{"a.xml": `<testsuite><testcase name="okay"/><testcase name="bad"><failure message="wrong value"/></testcase></testsuite>`, "b.txt": "test details"})
	_, client := setupArtifactServer(t, "owner", "repo", 123, "reports", data)
	first, err := client.GetArtifactContentWithOptions(context.Background(), 123, ArtifactReadOptions{ManifestOnly: true, Limit: 1})
	require.NoError(t, err)
	require.Equal(t, 2, first.FileCount)
	require.True(t, first.Truncated)
	require.NotNil(t, first.NextOffset)
	assert.Empty(t, first.Files[0].Content)
	second, err := client.GetArtifactContentWithOptions(context.Background(), 123, ArtifactReadOptions{ManifestOnly: true, Offset: *first.NextOffset})
	require.NoError(t, err)
	require.Equal(t, "b.txt", second.Files[0].Path)
	summary, err := client.GetArtifactContentWithOptions(context.Background(), 123, ArtifactReadOptions{SummaryOnly: true})
	require.NoError(t, err)
	require.Len(t, summary.Reports, 1)
	assert.Equal(t, 2, summary.Reports[0].Total)
	assert.Equal(t, 1, summary.Reports[0].Failures)
	assert.Empty(t, summary.Files[0].Content)
}

func TestArtifactsTraverseAPIPages(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/repo/actions/runs/1/artifacts", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			fmt.Fprint(w, `{"artifacts":[{"id":2,"name":"second"}]}`)
			return
		}
		w.Header().Set("Link", fmt.Sprintf(`<http://%s/repos/owner/repo/actions/runs/1/artifacts?page=2>; rel="next"`, r.Host))
		fmt.Fprint(w, `{"artifacts":[{"id":1,"name":"first"}]}`)
	})
	artifacts, err := newMuxClient(t, mux).GetWorkflowRunArtifacts(context.Background(), 1)
	require.NoError(t, err)
	require.Len(t, artifacts, 2)
	require.Equal(t, "second", artifacts[1].Name)
}

func TestArtifactContentPageBudget(t *testing.T) {
	data := makeArtifactZIP(t, map[string]string{"a.txt": strings.Repeat("a", 600), "b.txt": strings.Repeat("b", 600), "c.txt": strings.Repeat("c", 600)})
	_, client := setupArtifactServer(t, "owner", "repo", 123, "logs", data)
	page, err := client.GetArtifactContentWithOptions(context.Background(), 123, ArtifactReadOptions{MaxTotalBytes: 1024})
	require.NoError(t, err)
	require.Len(t, page.Files, 1)
	require.True(t, page.Truncated)
	require.Equal(t, 1, *page.NextOffset)
	next, err := client.GetArtifactContentWithOptions(context.Background(), 123, ArtifactReadOptions{MaxTotalBytes: 1024, Offset: *page.NextOffset})
	require.NoError(t, err)
	require.Equal(t, "b.txt", next.Files[0].Path)
}
