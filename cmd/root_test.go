package cmd

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunToolRejectsInvalidJSONArgs(t *testing.T) {
	restore := preserveCommandGlobals()
	defer restore()

	cfgFile = writeTestConfig(t, "token: test-token\n")
	toolArgsJSON = "{"
	logLevel = "info"

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	err := runTool(cmd, []string{"list_workflows"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse --args as JSON object")
}

func TestRunToolUsesJSONRepoArgs(t *testing.T) {
	restore := preserveCommandGlobals()
	defer restore()

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/example-org/example-repo/actions/workflows", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"total_count":1,"workflows":[{"id":1,"name":"CI","path":".github/workflows/ci.yml","state":"active"}]}`))
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	cfgFile = writeTestConfig(t, "token: test-token\napi_base_url: "+ts.URL+"/\nupload_url: "+ts.URL+"/\n")
	toolArgsJSON = `{"owner":"example-org","repo":"example-repo","limit":1}`
	logLevel = "info"

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	output := captureStdout(t, func() {
		require.NoError(t, runTool(cmd, []string{"list_workflows"}))
	})

	assert.Contains(t, output, `"name":"CI"`)
}

func TestRunToolReturnsUnknownToolError(t *testing.T) {
	restore := preserveCommandGlobals()
	defer restore()

	cfgFile = writeTestConfig(t, "token: test-token\n")
	toolArgsJSON = `{}`
	logLevel = "info"

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	err := runTool(cmd, []string{"missing_tool"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown tool "missing_tool"`)
}

func writeTestConfig(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.yaml")
	err := os.WriteFile(path, []byte(body), 0644)
	require.NoError(t, err)
	return path
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = writer

	fn()

	require.NoError(t, writer.Close())
	os.Stdout = oldStdout
	defer func() {
		_ = reader.Close()
	}()

	data, err := io.ReadAll(reader)
	require.NoError(t, err)
	return strings.TrimSpace(string(data))
}

func preserveCommandGlobals() func() {
	oldCfgFile := cfgFile
	oldRepoOwner := repoOwner
	oldRepoName := repoName
	oldToken := token
	oldLogLevel := logLevel
	oldToolArgsJSON := toolArgsJSON

	return func() {
		cfgFile = oldCfgFile
		repoOwner = oldRepoOwner
		repoName = oldRepoName
		token = oldToken
		logLevel = oldLogLevel
		toolArgsJSON = oldToolArgsJSON
	}
}
