package mcp

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/denysvitali/gh-actions-mcp/config"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// updateGolden regenerates mcp/testdata/tools.golden.json instead of asserting
// against it. Run with: go test ./mcp/ -run Golden -update
var updateGolden = flag.Bool("update", false, "rewrite the tool-schema golden file")

const toolsGoldenPath = "testdata/tools.golden.json"

// listRegisteredTools returns the tool list exactly as an MCP client sees it.
func listRegisteredTools(t *testing.T) []*mcp.Tool {
	t.Helper()

	app, err := NewMCPServer(&config.Config{
		Token:     "token",
		RepoOwner: "owner",
		RepoName:  "repo",
	}, logrus.New())
	require.NoError(t, err)
	t.Cleanup(func() { _ = app.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := app.GetServer().Connect(ctx, serverTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = serverSession.Close() })

	client := mcp.NewClient(&mcp.Implementation{Name: "schema-test", Version: "test"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = clientSession.Close() })

	tools, err := clientSession.ListTools(ctx, nil)
	require.NoError(t, err)
	return tools.Tools
}

// TestRegisteredToolSchemasGolden is the wire-compatibility gate for this
// package: it pins every registered tool name, description, annotation and
// input/output schema, in list order, against a checked-in snapshot. Any change
// to the MCP wire contract fails here.
func TestRegisteredToolSchemasGolden(t *testing.T) {
	tools := listRegisteredTools(t)

	actual, err := json.MarshalIndent(tools, "", "  ")
	require.NoError(t, err)
	actual = append(actual, '\n')

	if *updateGolden {
		require.NoError(t, os.MkdirAll(filepath.Dir(toolsGoldenPath), 0o755))
		require.NoError(t, os.WriteFile(toolsGoldenPath, actual, 0o644))
		t.Logf("wrote %s (%d bytes)", toolsGoldenPath, len(actual))
		return
	}

	expected, err := os.ReadFile(toolsGoldenPath)
	require.NoError(t, err, "golden file missing; regenerate with: go test ./mcp/ -run Golden -update")
	assert.Equal(t, string(expected), string(actual),
		"registered tool schemas drifted from %s — the MCP wire contract changed", toolsGoldenPath)
}

// TestRegisteredToolNames pins the tool set and its list order independently of
// the golden blob, so a name change produces a readable failure.
func TestRegisteredToolNames(t *testing.T) {
	tools := listRegisteredTools(t)

	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}

	assert.Equal(t, []string{
		"analyze_timing",
		"diagnose_failure",
		"download_artifact",
		"get_artifact",
		"get_check_status",
		"get_run",
		"list_runs",
		"list_workflows",
		"manage_run",
		"wait_all",
		"wait_for_commit_checks",
		"wait_for_run",
	}, names)
}

// TestGetRunElementEnumOrder pins the element enum of get_run. The enum order is
// part of the wire contract and must match runElements (run_elements.go), which
// the schema derives it from.
func TestGetRunElementEnumOrder(t *testing.T) {
	want := []any{"info", "jobs", "logs", "log_files", "log_sections", "artifacts", "artifact_content"}

	for _, tool := range listRegisteredTools(t) {
		if tool.Name != "get_run" {
			continue
		}
		schema, err := json.Marshal(tool.InputSchema)
		require.NoError(t, err)
		var decoded struct {
			Properties struct {
				Element struct {
					Enum    []any  `json:"enum"`
					Default string `json:"default"`
				} `json:"element"`
			} `json:"properties"`
		}
		require.NoError(t, json.Unmarshal(schema, &decoded))
		assert.Equal(t, want, decoded.Properties.Element.Enum)
		assert.Equal(t, "info", decoded.Properties.Element.Default)
		return
	}
	t.Fatal("get_run tool not registered")
}
