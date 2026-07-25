package cmd

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

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
		require.NoError(t, reader.Close())
	}()

	data, err := io.ReadAll(reader)
	require.NoError(t, err)
	return strings.TrimSpace(string(data))
}

// preserveCommandGlobals snapshots every package-level flag variable and
// returns a function that restores them. Cobra binds flags to globals, so
// tests that set them cannot run in parallel; restoring keeps them
// order-independent.
func preserveCommandGlobals() func() {
	oldVersion := version
	oldCfgFile := cfgFile
	oldRepoOwner := repoOwner
	oldRepoName := repoName
	oldToken := token
	oldLogLevel := logLevel
	oldNoGitProxyDetect := noGitProxyDetect
	oldToolArgsJSON := toolArgsJSON
	oldMCPTransport := mcpTransport
	oldMCPHTTPAddress := mcpHTTPAddress
	oldMCPHTTPPath := mcpHTTPPath
	oldMCPHTTPToken := mcpHTTPToken
	oldMCPHTTPTLSCert := mcpHTTPTLSCert
	oldMCPHTTPTLSKey := mcpHTTPTLSKey
	oldMCPHTTPOrigins := append([]string(nil), mcpHTTPOrigins...)
	oldMCPHTTPMaxBody := mcpHTTPMaxBody
	oldLogsSearch := logsSearch
	oldLogsRegex := logsRegex
	oldLogsSection := logsSection
	oldLogsContext := logsContext
	oldLogsTail := logsTail
	oldLogsHead := logsHead
	oldLogsOffset := logsOffset
	oldLogsNoHeaders := logsNoHeaders
	oldLogsJobID := logsJobID
	oldLogsOwner := logsOwner
	oldLogsRepo := logsRepo
	oldLogLevelValue := log.GetLevel()

	return func() {
		version = oldVersion
		cfgFile = oldCfgFile
		repoOwner = oldRepoOwner
		repoName = oldRepoName
		token = oldToken
		logLevel = oldLogLevel
		noGitProxyDetect = oldNoGitProxyDetect
		toolArgsJSON = oldToolArgsJSON
		mcpTransport = oldMCPTransport
		mcpHTTPAddress = oldMCPHTTPAddress
		mcpHTTPPath = oldMCPHTTPPath
		mcpHTTPToken = oldMCPHTTPToken
		mcpHTTPTLSCert = oldMCPHTTPTLSCert
		mcpHTTPTLSKey = oldMCPHTTPTLSKey
		mcpHTTPOrigins = oldMCPHTTPOrigins
		mcpHTTPMaxBody = oldMCPHTTPMaxBody
		logsSearch = oldLogsSearch
		logsRegex = oldLogsRegex
		logsSection = oldLogsSection
		logsContext = oldLogsContext
		logsTail = oldLogsTail
		logsHead = oldLogsHead
		logsOffset = oldLogsOffset
		logsNoHeaders = oldLogsNoHeaders
		logsJobID = oldLogsJobID
		logsOwner = oldLogsOwner
		logsRepo = oldLogsRepo
		log.SetLevel(oldLogLevelValue)
	}
}
