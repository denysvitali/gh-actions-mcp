package github

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestArtifactCacheReusesDownloadAndScopesClient(t *testing.T) {
	var downloads atomic.Int32
	data := makeArtifactZIP(t, map[string]string{"a.txt": "report details"})
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/repo/actions/artifacts/123", jsonHandler(`{"id":123,"name":"report"}`))
	mux.HandleFunc("/repos/owner/repo/actions/artifacts/123/zip", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "http://"+r.Host+"/blob")
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/blob", func(w http.ResponseWriter, r *http.Request) { downloads.Add(1); _, _ = w.Write(data) })
	first := newMuxClient(t, mux)
	_, err := first.GetArtifactContentWithOptions(context.Background(), 123, ArtifactReadOptions{ManifestOnly: true})
	require.NoError(t, err)
	content, err := first.GetArtifactContentWithOptions(context.Background(), 123, ArtifactReadOptions{})
	require.NoError(t, err)
	require.Equal(t, "report details", content.Files[0].Content)
	require.Equal(t, int32(1), downloads.Load())
	second := newMuxClient(t, mux)
	_, err = second.GetArtifactContentWithOptions(context.Background(), 123, ArtifactReadOptions{ManifestOnly: true})
	require.NoError(t, err)
	require.Equal(t, int32(2), downloads.Load(), "separate credential/client scopes must not reuse private artifact bytes")
}

func TestArtifactManifestDoesNotDecompressEntries(t *testing.T) {
	data := makeArtifactZIP(t, map[string]string{"large.log": strings.Repeat("a", 4096)})
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	require.NoError(t, err)
	offset, err := archive.File[0].DataOffset()
	require.NoError(t, err)
	data[offset] ^= 0xff // Invalid compressed entry; the central directory stays readable.
	_, client := setupArtifactServer(t, "owner", "repo", 123, "report", data)
	manifest, err := client.GetArtifactContentWithOptions(context.Background(), 123, ArtifactReadOptions{ManifestOnly: true})
	require.NoError(t, err)
	require.Len(t, manifest.Files, 1)
	require.Equal(t, int64(4096), manifest.Files[0].Size)
	require.Empty(t, manifest.Files[0].Content)
	content, err := client.GetArtifactContentWithOptions(context.Background(), 123, ArtifactReadOptions{})
	require.NoError(t, err)
	require.Contains(t, content.Files[0].Content, "could not be read")
}

func TestHistoricalFingerprintComparisonBoundAndScope(t *testing.T) {
	mux := http.NewServeMux()
	var downloads atomic.Int32
	history := []string{}
	for i := 2; i < 7; i++ {
		history = append(history, fmt.Sprintf(`{"id":%d,"status":"completed","conclusion":"failure","head_sha":"abc","event":"push","workflow_id":50}`, i))
		mux.HandleFunc(fmt.Sprintf("/repos/owner/repo/actions/runs/%d/jobs", i), jsonHandler(fmt.Sprintf(`{"jobs":[{"id":%d,"name":"build","conclusion":"failure"}]}`, i+100)))
	}
	history = append(history, `{"id":7,"status":"completed","conclusion":"failure","head_sha":"other","event":"push","workflow_id":50}`)
	mux.HandleFunc("/repos/owner/repo/actions/runs/7/jobs", jsonHandler(`{"jobs":[{"id":107,"name":"build","conclusion":"failure"}]}`))
	mux.HandleFunc("/repos/owner/repo/actions/workflows/50/runs", jsonHandler(`{"workflow_runs":[`+strings.Join(history, ",")+`]}`))
	mux.HandleFunc("/repos/owner/repo/actions/jobs/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "http://"+r.Host+"/blob")
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/blob", func(w http.ResponseWriter, r *http.Request) {
		downloads.Add(1)
		fmt.Fprint(w, "progress\nerror: undefined: Widget\n##[error]Process completed with exit code 1.\n")
	})
	current := []*FailedJob{{JobName: "build", Fingerprint: diagnosticFingerprint([]string{"error: undefined: Widget"})}}
	info := newMuxClient(t, mux).checkFlakiness(context.Background(), &WorkflowRun{ID: 1, WorkflowID: 50, HeadSHA: "abc", Event: "push"}, current)
	require.Equal(t, 3, info.FingerprintComparisons)
	require.Equal(t, 3, info.MatchingFingerprints)
	require.Equal(t, int32(3), downloads.Load())
	require.Empty(t, info.Samples[len(info.Samples)-1].Fingerprints, "different commits are never fingerprint-comparable")
}
