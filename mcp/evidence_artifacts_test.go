package mcp

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/denysvitali/gh-actions-mcp/config"
	"github.com/denysvitali/gh-actions-mcp/github"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func TestArtifactEvidencePagesInsideFiles(t *testing.T) {
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	original := map[string]string{"a.txt": strings.Repeat("first line\n", 50), "b.txt": strings.Repeat("世", 200)}
	for _, name := range []string{"a.txt", "b.txt"} {
		file, err := writer.Create(name)
		require.NoError(t, err)
		_, err = file.Write([]byte(original[name]))
		require.NoError(t, err)
	}
	require.NoError(t, writer.Close())
	mux := http.NewServeMux()
	var base string
	mux.HandleFunc("/repos/o/r/actions/artifacts/123", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":123,"name":"evidence","size_in_bytes":%d}`, archive.Len())
	})
	mux.HandleFunc("/repos/o/r/actions/artifacts/123/zip", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, base+"/blob", http.StatusFound) })
	mux.HandleFunc("/blob", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(archive.Bytes()) })
	remote := httptest.NewServer(mux)
	defer remote.Close()
	base = remote.URL
	server, err := NewMCPServer(&config.Config{Token: "test", RepoOwner: "o", RepoName: "r", APIBaseURL: base + "/"}, logrus.New())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, server.Close()) })
	input := artifactInput{ArtifactID: 123, evidenceInput: evidenceInput{MaxBytes: 256, MaxLines: 10}}
	received := map[string]string{}
	for pages := 0; ; pages++ {
		require.Less(t, pages, 20, "pagination must make progress")
		_, result, err := server.getArtifactTyped(context.Background(), nil, input)
		require.NoError(t, err)
		require.LessOrEqual(t, result.ReturnedBytes, 256)
		require.LessOrEqual(t, result.ReturnedLines, 10)
		for _, file := range result.Files {
			received[file.Path] += file.Content
		}
		if result.NextCursor == "" {
			require.False(t, result.Truncated)
			break
		}
		require.True(t, result.Truncated)
		input.Cursor = result.NextCursor
	}
	require.Equal(t, original, received)
}

func TestArtifactSummaryEvidenceUsesContentBudget(t *testing.T) {
	content := &github.ArtifactContent{
		Files:   []*github.ArtifactFile{{Path: "junit.xml"}},
		Reports: []github.ArtifactReportSummary{{Path: "junit.xml", Format: "junit", Total: 20, Failures: 20, Examples: []string{strings.Repeat("failure ", 100)}}},
	}
	prepareReportEvidence(content)
	result, next, err := artifactEvidencePage(content, cursorPosition{Page: 1}, 256, 10)
	require.NoError(t, err)
	require.Equal(t, 256, result.ReturnedBytes)
	require.Equal(t, 256, next.Offset)
	projectReportEvidence(&result, content.Reports)
	require.Empty(t, result.Files[0].Content)
	require.Len(t, result.Reports, 1)
	require.Equal(t, 20, result.Reports[0].Failures)
	require.Len(t, strings.Join(result.Reports[0].Examples, "\n"), 256)
}
