package mcp

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/denysvitali/gh-actions-mcp/config"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const stdioHelperEnv = "GH_ACTIONS_MCP_STDIO_HELPER"

func TestMain(m *testing.M) {
	if os.Getenv(stdioHelperEnv) == "1" {
		server, err := NewMCPServer(&config.Config{Token: "token", RepoOwner: "owner", RepoName: "repo", ServerVersion: "stdio-test"}, logrus.New())
		if err != nil {
			os.Exit(2)
		}
		err = server.GetServer().Run(context.Background(), &sdkmcp.StdioTransport{})
		if err != nil {
			os.Exit(3)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestStdioProcessConformance(t *testing.T) {
	executable, err := os.Executable()
	require.NoError(t, err)
	command := exec.Command(executable)
	command.Env = append(os.Environ(), stdioHelperEnv+"=1")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "stdio-conformance-client", Version: "test"}, nil)
	session, err := client.Connect(ctx, &sdkmcp.CommandTransport{Command: command}, nil)
	require.NoError(t, err)
	defer session.Close()
	assert.Equal(t, "stdio-test", session.InitializeResult().ServerInfo.Version)

	tools, err := session.ListTools(ctx, nil)
	require.NoError(t, err)
	assert.Len(t, tools.Tools, 12)
	templates, err := session.ListResourceTemplates(ctx, nil)
	require.NoError(t, err)
	assert.Len(t, templates.ResourceTemplates, 1)
	result, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "get_run", Arguments: map[string]any{}})
	require.NoError(t, err)
	assert.True(t, result.IsError)
}
