package github

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	githubapi "github.com/google/go-github/v89/github"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeArtifactZIP(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		f, err := zw.Create(name)
		require.NoError(t, err)
		_, err = io.WriteString(f, content)
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

// artifactJSON returns a JSON body matching the go-github Artifact model.
func artifactJSON(id int64, name string, size int64) []byte {
	m := map[string]interface{}{
		"id":                   id,
		"name":                 name,
		"size_in_bytes":        size,
		"archive_download_url": "",
	}
	b, _ := json.Marshal(m)
	return b
}

// setupArtifactServer creates an httptest server that handles the GetArtifact
// and DownloadArtifact GitHub API endpoints plus a pre-signed blob endpoint
// that rejects requests carrying an Authorization header.
func setupArtifactServer(t *testing.T, owner, repo string, artifactID int64, artifactName string, zipData []byte) (*httptest.Server, *Client) {
	t.Helper()

	if log == nil {
		SetLogger(logrus.New())
	}

	mux := http.NewServeMux()
	redirectBase := ""

	// GET /repos/{owner}/{repo}/actions/artifacts/{id} — metadata
	mux.HandleFunc(
		"/repos/"+owner+"/"+repo+"/actions/artifacts/123",
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(artifactJSON(artifactID, artifactName, int64(len(zipData))))
		},
	)

	// GET /repos/{owner}/{repo}/actions/artifacts/{id}/zip — redirect to blob
	mux.HandleFunc(
		"/repos/"+owner+"/"+repo+"/actions/artifacts/123/zip",
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", redirectBase+"/blob/artifact.zip")
			w.WriteHeader(http.StatusFound)
		},
	)

	// GET /blob/artifact.zip — pre-signed URL: must NOT carry Authorization
	mux.HandleFunc("/blob/artifact.zip", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte("InvalidAuthenticationInfo"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(zipData)
	})

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
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
	return ts, client
}

func TestGetArtifactContent_WithoutAuthHeader(t *testing.T) {
	const (
		owner = "test-owner"
		repo  = "test-repo"
	)

	zipData := makeArtifactZIP(t, map[string]string{
		"hello.txt":  "hello world\n",
		"sub/app.go": "package main\n",
	})

	_, client := setupArtifactServer(t, owner, repo, 123, "my-artifact", zipData)

	content, err := client.GetArtifactContent(context.Background(), 123, "", 0)
	require.NoError(t, err)

	assert.Equal(t, "my-artifact", content.Name)
	assert.Equal(t, int64(123), content.ID)
	assert.Equal(t, 2, content.FileCount)

	// Files are sorted by path
	require.Len(t, content.Files, 2)
	assert.Equal(t, "hello.txt", content.Files[0].Path)
	assert.Equal(t, "hello world\n", content.Files[0].Content)
	assert.Equal(t, "text", content.Files[0].Encoding)
	assert.Equal(t, "sub/app.go", content.Files[1].Path)
	assert.Equal(t, "package main\n", content.Files[1].Content)
}

func TestGetArtifactContent_FilePattern(t *testing.T) {
	const (
		owner = "test-owner"
		repo  = "test-repo"
	)

	zipData := makeArtifactZIP(t, map[string]string{
		"result.txt":  "ok",
		"result.json": `{"status":"ok"}`,
	})

	_, client := setupArtifactServer(t, owner, repo, 123, "results", zipData)

	content, err := client.GetArtifactContent(context.Background(), 123, "*.json", 0)
	require.NoError(t, err)

	require.Len(t, content.Files, 1)
	assert.Equal(t, "result.json", content.Files[0].Path)
}

func TestGetArtifactContent_MaxFileSize(t *testing.T) {
	const (
		owner = "test-owner"
		repo  = "test-repo"
	)

	zipData := makeArtifactZIP(t, map[string]string{
		"small.txt": "hi",
		"large.txt": strings.Repeat("x", 1000),
	})

	_, client := setupArtifactServer(t, owner, repo, 123, "sized", zipData)

	content, err := client.GetArtifactContent(context.Background(), 123, "", 500)
	require.NoError(t, err)

	require.Len(t, content.Files, 2)
	// Files are sorted by path: large.txt before small.txt
	assert.Equal(t, "large.txt", content.Files[0].Path)
	assert.Contains(t, content.Files[0].Content, "file too large")
	assert.Equal(t, "small.txt", content.Files[1].Path)
	assert.Equal(t, "hi", content.Files[1].Content)
}

func TestDownloadArtifact_WithoutAuthHeader(t *testing.T) {
	const (
		owner = "test-owner"
		repo  = "test-repo"
	)

	zipData := makeArtifactZIP(t, map[string]string{
		"data.csv": "a,b,c\n1,2,3\n",
	})

	_, client := setupArtifactServer(t, owner, repo, 123, "csv-export", zipData)

	outDir := t.TempDir()
	outPath := filepath.Join(outDir, "artifact.zip")

	result, err := client.DownloadArtifact(context.Background(), 123, outPath)
	require.NoError(t, err)

	assert.Equal(t, "csv-export", result.Name)
	assert.Equal(t, int64(123), result.ID)
	assert.Equal(t, outPath, result.SavedPath)
	assert.Equal(t, 1, result.FileCount)
	assert.Equal(t, int64(len(zipData)), result.TotalSize)

	// Verify the file on disk is a valid ZIP with the expected content
	saved, err := os.ReadFile(outPath)
	require.NoError(t, err)
	assert.Equal(t, zipData, saved)
}

func TestDownloadArtifact_DefaultOutputPath(t *testing.T) {
	const (
		owner = "test-owner"
		repo  = "test-repo"
	)

	zipData := makeArtifactZIP(t, map[string]string{"f.txt": "x"})

	_, client := setupArtifactServer(t, owner, repo, 123, "my-artifact", zipData)

	// Use empty outputPath — function should derive "my-artifact.zip"
	// Run in a temp dir so we don't pollute the repo.
	origDir, _ := os.Getwd()
	tmpDir := t.TempDir()
	require.NoError(t, os.Chdir(tmpDir))
	t.Cleanup(func() { _ = os.Chdir(origDir) })

	result, err := client.DownloadArtifact(context.Background(), 123, "")
	require.NoError(t, err)

	assert.Equal(t, "my-artifact.zip", result.SavedPath)
	_, err = os.Stat(filepath.Join(tmpDir, "my-artifact.zip"))
	assert.NoError(t, err)
}

func TestDownloadArtifact_RootConfinementAndExplicitOverwrite(t *testing.T) {
	zipData := makeArtifactZIP(t, map[string]string{"fresh.txt": "new"})
	_, client := setupArtifactServer(t, "test-owner", "test-repo", 123, "safe-artifact", zipData)
	root := t.TempDir()
	destination := filepath.Join(root, "artifact.zip")
	require.NoError(t, os.WriteFile(destination, []byte("original"), 0o600))

	_, err := client.DownloadArtifactWithOptions(context.Background(), 123, ArtifactDownloadOptions{
		Root: root, OutputPath: "artifact.zip",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
	original, readErr := os.ReadFile(destination)
	require.NoError(t, readErr)
	assert.Equal(t, []byte("original"), original)

	result, err := client.DownloadArtifactWithOptions(context.Background(), 123, ArtifactDownloadOptions{
		Root: root, OutputPath: "artifact.zip", Overwrite: true,
	})
	require.NoError(t, err)
	assert.Equal(t, destination, result.SavedPath)
	saved, readErr := os.ReadFile(destination)
	require.NoError(t, readErr)
	assert.Equal(t, zipData, saved)

	_, err = client.DownloadArtifactWithOptions(context.Background(), 123, ArtifactDownloadOptions{
		Root: root, OutputPath: "../escape.zip",
	})
	assert.ErrorContains(t, err, "must stay beneath artifact root")
}

// Tests for DiagnoseFailure functionality

func TestIsTextContent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		data []byte
		want bool
	}{
		{name: "empty is text", data: nil, want: true},
		{name: "ASCII is text", data: []byte("hello world"), want: true},
		{name: "UTF-8 is text", data: []byte("héllo → wörld"), want: true},
		{name: "a NUL byte in the first 512 makes it binary", data: []byte("abc\x00def"), want: false},
		{
			// Only the first 512 bytes are sampled, so a NUL past that is missed.
			name: "a NUL byte past the first 512 bytes is not detected",
			data: append(bytes.Repeat([]byte("a"), 512), 0x00),
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, isTextContent(tt.data))
		})
	}
}

func TestGetWorkflowRunArtifacts(t *testing.T) {
	t.Parallel()

	t.Run("maps every artifact in the response", func(t *testing.T) {
		t.Parallel()

		mux := http.NewServeMux()
		mux.HandleFunc("/repos/owner/repo/actions/runs/1/artifacts", jsonHandler(`{"total_count":2,"artifacts":[
			{"id":10,"name":"logs","size_in_bytes":100,"created_at":"2026-01-01T00:00:00Z","expires_at":"2026-02-01T00:00:00Z","archive_download_url":"https://example.com/10"},
			{"id":11,"name":"coverage","size_in_bytes":200}
		]}`))
		artifacts, err := newMuxClient(t, mux).GetWorkflowRunArtifacts(context.Background(), 1)
		require.NoError(t, err)
		require.Len(t, artifacts, 2)
		assert.Equal(t, int64(10), artifacts[0].ID)
		assert.Equal(t, "logs", artifacts[0].Name)
		assert.Equal(t, int64(100), artifacts[0].SizeInBytes)
		assert.Equal(t, "https://example.com/10", artifacts[0].ArchiveURL)
		assert.NotEmpty(t, artifacts[0].CreatedAt)
	})

	t.Run("an API failure is wrapped", func(t *testing.T) {
		t.Parallel()

		mux := http.NewServeMux()
		mux.HandleFunc("/repos/owner/repo/actions/runs/1/artifacts", statusHandler(http.StatusForbidden))
		_, err := newMuxClient(t, mux).GetWorkflowRunArtifacts(context.Background(), 1)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to list artifacts for run 1")
	})
}

func TestGetArtifactByID_Error(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/repo/actions/artifacts/123", statusHandler(http.StatusNotFound))
	_, err := newMuxClient(t, mux).GetArtifactByID(context.Background(), 10)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get artifact 10")
}

func TestGetArtifactContent_Errors(t *testing.T) {
	t.Parallel()

	t.Run("metadata lookup failure short-circuits", func(t *testing.T) {
		t.Parallel()

		mux := http.NewServeMux()
		mux.HandleFunc("/repos/owner/repo/actions/artifacts/123", statusHandler(http.StatusNotFound))
		_, err := newMuxClient(t, mux).GetArtifactContent(context.Background(), 10, "", 0)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to get artifact 10")
	})

	t.Run("download URL failure is wrapped", func(t *testing.T) {
		t.Parallel()

		mux := http.NewServeMux()
		mux.HandleFunc("/repos/owner/repo/actions/artifacts/123", jsonHandler(`{"id":123,"name":"logs","size_in_bytes":1}`))
		mux.HandleFunc("/repos/owner/repo/actions/artifacts/123/zip", statusHandler(http.StatusGone))
		_, err := newMuxClient(t, mux).GetArtifactContent(context.Background(), 123, "", 0)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to get artifact download URL")
	})

	t.Run("a non-ZIP payload is rejected", func(t *testing.T) {
		t.Parallel()

		base := ""
		mux := http.NewServeMux()
		mux.HandleFunc("/repos/owner/repo/actions/artifacts/123", jsonHandler(`{"id":123,"name":"logs","size_in_bytes":1}`))
		mux.HandleFunc("/repos/owner/repo/actions/artifacts/123/zip", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", base+"/blob/a.zip")
			w.WriteHeader(http.StatusFound)
		})
		mux.HandleFunc("/blob/a.zip", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("not a zip"))
		})
		client, url := newMuxClientWithURL(t, mux)
		base = url

		_, err := client.GetArtifactContent(context.Background(), 123, "", 0)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to open artifact archive")
	})

	t.Run("a non-200 pre-signed response is rejected", func(t *testing.T) {
		t.Parallel()

		base := ""
		mux := http.NewServeMux()
		mux.HandleFunc("/repos/owner/repo/actions/artifacts/123", jsonHandler(`{"id":123,"name":"logs","size_in_bytes":1}`))
		mux.HandleFunc("/repos/owner/repo/actions/artifacts/123/zip", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", base+"/blob/a.zip")
			w.WriteHeader(http.StatusFound)
		})
		mux.HandleFunc("/blob/a.zip", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		})
		client, url := newMuxClientWithURL(t, mux)
		base = url

		_, err := client.GetArtifactContent(context.Background(), 123, "", 0)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to fetch artifact: HTTP 403")
	})

	t.Run("an invalid file pattern is reported", func(t *testing.T) {
		t.Parallel()

		_, client := setupArtifactServer(t, "owner", "repo", 123, "logs", makeArtifactZIP(t, map[string]string{"a.txt": "x"}))
		_, err := client.GetArtifactContent(context.Background(), 123, "[", 0)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `invalid file pattern "["`)
	})

	t.Run("binary files are base64 encoded", func(t *testing.T) {
		t.Parallel()

		_, client := setupArtifactServer(t, "owner", "repo", 123, "logs", makeArtifactZIP(t, map[string]string{
			"bin.dat": "a\x00b",
			"txt.log": "plain",
		}))
		content, err := client.GetArtifactContent(context.Background(), 123, "", 0)
		require.NoError(t, err)
		require.Len(t, content.Files, 2)
		assert.Equal(t, "base64", content.Files[0].Encoding)
		assert.Equal(t, base64.StdEncoding.EncodeToString([]byte("a\x00b")), content.Files[0].Content)
		assert.Equal(t, "text", content.Files[1].Encoding)
		assert.Equal(t, "plain", content.Files[1].Content)
	})
}

func TestDownloadArtifactWithOptions_Errors(t *testing.T) {
	t.Parallel()

	zipData := makeArtifactZIP(t, map[string]string{"a.txt": "hello"})

	t.Run("metadata lookup failure short-circuits", func(t *testing.T) {
		t.Parallel()

		mux := http.NewServeMux()
		mux.HandleFunc("/repos/owner/repo/actions/artifacts/123", statusHandler(http.StatusNotFound))
		_, err := newMuxClient(t, mux).DownloadArtifactWithOptions(context.Background(), 10, ArtifactDownloadOptions{Root: t.TempDir()})
		require.Error(t, err)
	})

	t.Run("an escaping output path is refused before any network call", func(t *testing.T) {
		t.Parallel()

		_, client := setupArtifactServer(t, "owner", "repo", 123, "logs", zipData)
		_, err := client.DownloadArtifactWithOptions(context.Background(), 123, ArtifactDownloadOptions{
			Root:       t.TempDir(),
			OutputPath: "../escape.zip",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must stay beneath artifact root")
	})

	t.Run("an existing destination is preserved unless Overwrite is set", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(root, "a.zip"), []byte("original"), 0o600))

		_, client := setupArtifactServer(t, "owner", "repo", 123, "logs", zipData)
		_, err := client.DownloadArtifactWithOptions(context.Background(), 123, ArtifactDownloadOptions{Root: root, OutputPath: "a.zip"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "already exists; set overwrite=true")

		kept, err := os.ReadFile(filepath.Join(root, "a.zip"))
		require.NoError(t, err)
		assert.Equal(t, "original", string(kept))
	})

	t.Run("Overwrite replaces an existing destination atomically", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(root, "a.zip"), []byte("original"), 0o600))

		_, client := setupArtifactServer(t, "owner", "repo", 123, "logs", zipData)
		result, err := client.DownloadArtifactWithOptions(context.Background(), 123, ArtifactDownloadOptions{
			Root: root, OutputPath: "a.zip", Overwrite: true,
		})
		require.NoError(t, err)
		assert.Equal(t, 1, result.FileCount)
		assert.Equal(t, int64(len(zipData)), result.TotalSize)

		written, err := os.ReadFile(filepath.Join(root, "a.zip"))
		require.NoError(t, err)
		assert.Equal(t, zipData, written)
	})

	t.Run("nested output directories are created inside the root", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		_, client := setupArtifactServer(t, "owner", "repo", 123, "logs", zipData)
		result, err := client.DownloadArtifactWithOptions(context.Background(), 123, ArtifactDownloadOptions{
			Root: root, OutputPath: "deep/nested/a.zip",
		})
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(root, "deep/nested/a.zip"), result.SavedPath)
		assert.FileExists(t, filepath.Join(root, "deep/nested/a.zip"))

		// No temporary file survives a successful publication.
		entries, err := os.ReadDir(filepath.Join(root, "deep/nested"))
		require.NoError(t, err)
		require.Len(t, entries, 1)
	})

	t.Run("the artifact name is sanitised into the default output path", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		_, client := setupArtifactServer(t, "owner", "repo", 123, "logs/nested", zipData)
		result, err := client.DownloadArtifactWithOptions(context.Background(), 123, ArtifactDownloadOptions{Root: root})
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(root, "logs-nested.zip"), result.SavedPath)
	})

	t.Run("a payload that is not a ZIP still downloads, reporting zero files", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		_, client := setupArtifactServer(t, "owner", "repo", 123, "logs", []byte("not a zip"))
		result, err := client.DownloadArtifactWithOptions(context.Background(), 123, ArtifactDownloadOptions{Root: root, OutputPath: "a.zip"})
		require.NoError(t, err)
		assert.Zero(t, result.FileCount, "an unreadable archive is not an error, it just has no counted files")
		assert.Equal(t, int64(len("not a zip")), result.TotalSize)
	})

	t.Run("a non-200 pre-signed response is rejected", func(t *testing.T) {
		t.Parallel()

		base := ""
		mux := http.NewServeMux()
		mux.HandleFunc("/repos/owner/repo/actions/artifacts/123", jsonHandler(`{"id":123,"name":"logs","size_in_bytes":1}`))
		mux.HandleFunc("/repos/owner/repo/actions/artifacts/123/zip", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", base+"/blob/a.zip")
			w.WriteHeader(http.StatusFound)
		})
		mux.HandleFunc("/blob/a.zip", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		})
		client, url := newMuxClientWithURL(t, mux)
		base = url

		_, err := client.DownloadArtifactWithOptions(context.Background(), 123, ArtifactDownloadOptions{Root: t.TempDir()})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to fetch artifact: HTTP 403")
	})
}

func TestDownloadArtifact_AbsolutePathSplitsRootAndName(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	zipData := makeArtifactZIP(t, map[string]string{"a.txt": "hello"})
	_, client := setupArtifactServer(t, "owner", "repo", 123, "logs", zipData)

	result, err := client.DownloadArtifact(context.Background(), 123, filepath.Join(root, "out.zip"))
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, "out.zip"), result.SavedPath)
	assert.FileExists(t, filepath.Join(root, "out.zip"))
}
