package cmd

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The runLogs tests below mutate package-level flag variables (cfgFile,
// logsJobID, logsSection, …) because cobra binds flags to globals. They must
// therefore NOT call t.Parallel(): two of them running concurrently would see
// each other's flag values. preserveCommandGlobals restores the values so the
// tests stay order-independent.

// logsZip builds a run/job log archive with a single file.
func logsZip(t *testing.T, name, body string) []byte {
	t.Helper()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	f, err := zw.Create(name)
	require.NoError(t, err)
	_, err = io.WriteString(f, body)
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

// logsServer serves the two GitHub log endpoints runLogs can reach and records
// which API paths were requested.
type logsServer struct {
	*httptest.Server

	//nolint:forbidigo // guards paths (below); sole lock, no ordering to respect
	mu    sync.Mutex
	paths []string
}

func (s *logsServer) requested(path string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.paths {
		if p == path {
			return true
		}
	}
	return false
}

func newLogsServer(t *testing.T, body string) *logsServer {
	t.Helper()

	srv := &logsServer{}
	archive := logsZip(t, "1_step.txt", body)

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		srv.mu.Lock()
		srv.paths = append(srv.paths, r.URL.Path)
		srv.mu.Unlock()

		switch {
		case strings.HasSuffix(r.URL.Path, "/logs"):
			w.Header().Set("Location", srv.URL+"/blob/logs.zip")
			w.WriteHeader(http.StatusFound)
		case r.URL.Path == "/blob/logs.zip":
			_, _ = w.Write(archive)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
		}
	})

	srv.Server = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// logsTestConfig points the CLI at a local server with an explicit
// api_base_url, which also disables git proxy detection.
func logsTestConfig(t *testing.T, srv *logsServer, extra string) {
	t.Helper()

	cfgFile = writeTestConfig(t, "token: test-token\n"+
		"api_base_url: "+srv.URL+"/\n"+
		"upload_url: "+srv.URL+"/\n"+extra)
}

func newLogsCommand() *cobra.Command {
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	return cmd
}

func TestRunLogs_ResolvesRunURLToOwnerRepoAndRunID(t *testing.T) {
	defer preserveCommandGlobals()()

	srv := newLogsServer(t, "url-run-line\n")
	logsTestConfig(t, srv, "repo_owner: config-owner\nrepo_name: config-repo\n")
	logLevel = "info"

	output := captureStdout(t, func() {
		require.NoError(t, runLogs(newLogsCommand(),
			[]string{"https://github.com/url-owner/url-repo/actions/runs/555"}))
	})

	assert.Contains(t, output, "url-run-line")
	assert.True(t, srv.requested("/repos/url-owner/url-repo/actions/runs/555/logs"),
		"owner/repo/run id must come from the URL, not from the config")
}

func TestRunLogs_ResolvesJobURLToJobEndpoint(t *testing.T) {
	defer preserveCommandGlobals()()

	srv := newLogsServer(t, "url-job-line\n")
	logsTestConfig(t, srv, "repo_owner: config-owner\nrepo_name: config-repo\n")
	logLevel = "info"

	output := captureStdout(t, func() {
		require.NoError(t, runLogs(newLogsCommand(),
			[]string{"https://github.com/url-owner/url-repo/actions/runs/555/job/999"}))
	})

	assert.Contains(t, output, "url-job-line")
	assert.True(t, srv.requested("/repos/url-owner/url-repo/actions/jobs/999/logs"),
		"a job URL must fetch the job log endpoint")
}

func TestRunLogs_BareRunIDUsesConfigRepo(t *testing.T) {
	defer preserveCommandGlobals()()

	srv := newLogsServer(t, "bare-run-line\n")
	logsTestConfig(t, srv, "repo_owner: config-owner\nrepo_name: config-repo\n")
	logLevel = "info"

	output := captureStdout(t, func() {
		require.NoError(t, runLogs(newLogsCommand(), []string{"777"}))
	})

	assert.Contains(t, output, "bare-run-line")
	assert.True(t, srv.requested("/repos/config-owner/config-repo/actions/runs/777/logs"))
}

func TestRunLogs_JobIDFlagWinsOverRunEndpoint(t *testing.T) {
	defer preserveCommandGlobals()()

	srv := newLogsServer(t, "flag-job-line\n")
	logsTestConfig(t, srv, "repo_owner: config-owner\nrepo_name: config-repo\n")
	logLevel = "info"
	logsJobID = 4242

	output := captureStdout(t, func() {
		require.NoError(t, runLogs(newLogsCommand(), []string{"777"}))
	})

	assert.Contains(t, output, "flag-job-line")
	assert.True(t, srv.requested("/repos/config-owner/config-repo/actions/jobs/4242/logs"),
		"--job-id with a bare run id must fetch the job log endpoint")
}

func TestRunLogs_OwnerRepoFlagsOverrideConfig(t *testing.T) {
	defer preserveCommandGlobals()()

	srv := newLogsServer(t, "override-line\n")
	logsTestConfig(t, srv, "repo_owner: config-owner\nrepo_name: config-repo\n")
	logLevel = "info"
	logsOwner = "flag-owner"
	logsRepo = "flag-repo"

	output := captureStdout(t, func() {
		require.NoError(t, runLogs(newLogsCommand(), []string{"777"}))
	})

	assert.Contains(t, output, "override-line")
	assert.True(t, srv.requested("/repos/flag-owner/flag-repo/actions/runs/777/logs"))
}

func TestRunLogs_SectionUsesSectionExtraction(t *testing.T) {
	defer preserveCommandGlobals()()

	srv := newLogsServer(t, "before\n##[group]Build\ninside-build\n##[endgroup]\nafter\n")
	logsTestConfig(t, srv, "repo_owner: config-owner\nrepo_name: config-repo\n")
	logLevel = "info"
	logsSection = "Build"

	output := captureStdout(t, func() {
		require.NoError(t, runLogs(newLogsCommand(), []string{"777"}))
	})

	assert.Contains(t, output, "inside-build")
	assert.NotContains(t, output, "after")
}

func TestRunLogs_SearchFiltersLines(t *testing.T) {
	defer preserveCommandGlobals()()

	srv := newLogsServer(t, "keep this one\ndrop that one\n")
	logsTestConfig(t, srv, "repo_owner: config-owner\nrepo_name: config-repo\n")
	logLevel = "info"
	logsSearch = "keep"

	output := captureStdout(t, func() {
		require.NoError(t, runLogs(newLogsCommand(), []string{"777"}))
	})

	assert.Contains(t, output, "keep this one")
	assert.NotContains(t, output, "drop that one")
}

func TestRunLogs_NoMatchingLogsPrintsPlaceholder(t *testing.T) {
	defer preserveCommandGlobals()()

	srv := newLogsServer(t, "only this line\n")
	logsTestConfig(t, srv, "repo_owner: config-owner\nrepo_name: config-repo\n")
	logLevel = "info"
	logsSearch = "nothing-matches-this"

	output := captureStdout(t, func() {
		require.NoError(t, runLogs(newLogsCommand(), []string{"777"}))
	})

	assert.Equal(t, "(no matching logs)", output)
}

func TestRunLogs_ArgumentResolutionErrors(t *testing.T) {
	tests := []struct {
		name      string
		arg       string
		extraCfg  string
		wantError string
	}{
		{
			// A branch name is not a valid argument: runLogs only accepts an
			// Actions URL or a numeric id.
			name:      "branch name is rejected as a run id",
			arg:       "main",
			extraCfg:  "repo_owner: config-owner\nrepo_name: config-repo\n",
			wantError: "invalid run ID",
		},
		{
			name:      "non numeric argument is rejected as a run id",
			arg:       "123abc",
			extraCfg:  "repo_owner: config-owner\nrepo_name: config-repo\n",
			wantError: "invalid run ID",
		},
		{
			name:      "github url that is not an actions url is rejected as a run id",
			arg:       "https://github.com/owner/repo",
			extraCfg:  "repo_owner: config-owner\nrepo_name: config-repo\n",
			wantError: "invalid run ID",
		},
	}

	for _, tt := range tests {
		// No t.Parallel(): runLogs reads package-level flag variables.
		t.Run(tt.name, func(t *testing.T) {
			defer preserveCommandGlobals()()

			srv := newLogsServer(t, "unused\n")
			logsTestConfig(t, srv, tt.extraCfg)
			logLevel = "info"

			err := runLogs(newLogsCommand(), []string{tt.arg})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantError)
		})
	}
}

// Note: runLogs' own "repository owner and name must be specified via URL,
// config, or --owner/--repo flags" check is unreachable today — loadConfig
// already fails when either is empty, and the --owner/--repo flags can only
// set non-empty values. The reachable failure is the loadConfig one below.
func TestRunLogs_RequiresRepoFromConfig(t *testing.T) {
	defer preserveCommandGlobals()()
	withIsolatedGitConfig(t, "[user]\n\tname = Nobody\n")

	srv := newLogsServer(t, "unused\n")
	logsTestConfig(t, srv, "")
	logLevel = "info"

	err := runLogs(newLogsCommand(), []string{"777"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to load config")
	assert.Contains(t, err.Error(), "repository owner is required")
}

func TestRunLogs_NotFoundMentionsRunIDAndRepo(t *testing.T) {
	defer preserveCommandGlobals()()

	// newLogsServer serves any path ending in /logs, so it cannot produce a
	// 404 for the logs endpoint. Use a server that refuses everything.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	}))
	defer ts.Close()

	cfgFile = writeTestConfig(t, "token: test-token\n"+
		"api_base_url: "+ts.URL+"/\nupload_url: "+ts.URL+"/\n"+
		"repo_owner: config-owner\nrepo_name: config-repo\n")
	logLevel = "info"

	err := runLogs(newLogsCommand(), []string{"777"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "run or job not found (404)")
	assert.Contains(t, err.Error(), "777")
	assert.Contains(t, err.Error(), "config-owner/config-repo")
}

func TestRunLogs_UnauthorizedMentionsRepo(t *testing.T) {
	defer preserveCommandGlobals()()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
	}))
	defer ts.Close()

	cfgFile = writeTestConfig(t, "token: test-token\n"+
		"api_base_url: "+ts.URL+"/\nupload_url: "+ts.URL+"/\n"+
		"repo_owner: config-owner\nrepo_name: config-repo\n")
	logLevel = "info"

	err := runLogs(newLogsCommand(), []string{"777"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "authentication failed (401)")
	assert.Contains(t, err.Error(), "config-owner/config-repo")
}
