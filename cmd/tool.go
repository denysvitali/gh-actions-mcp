package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	appmcp "github.com/denysvitali/gh-actions-mcp/mcp"

	mcptypes "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
)

var toolCmd = &cobra.Command{
	Use:   "tool <tool-name>",
	Short: "Invoke an MCP tool locally",
	Long: `Invoke a registered MCP tool locally using a JSON argument object.

Examples:
  gh-actions-mcp tool list_runs --args '{"owner":"example","repo":"demo","per_page":10}'
  gh-actions-mcp tool analyze_timing --args '{"owner":"example","repo":"demo","workflow":"CI","limit":10}'`,
	Args: cobra.ExactArgs(1),
	RunE: runTool,
}

func runTool(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	if err := configureLogLevel(); err != nil {
		return err
	}

	cfg, err := loadConfigAllowMissingRepo()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	toolArgs := map[string]interface{}{}
	if strings.TrimSpace(toolArgsJSON) != "" {
		if err := json.Unmarshal([]byte(toolArgsJSON), &toolArgs); err != nil {
			return fmt.Errorf("failed to parse --args as JSON object: %w", err)
		}
		if toolArgs == nil {
			toolArgs = map[string]interface{}{}
		}
	}

	mcpServer, err := appmcp.NewMCPServer(cfg, log)
	if err != nil {
		return err
	}
	defer func() { _ = mcpServer.Close() }()
	result, err := mcpServer.InvokeTool(ctx, args[0], toolArgs)
	if err != nil {
		return err
	}

	if result.IsError {
		return errors.New(renderToolResult(result))
	}

	output := renderToolResult(result)
	if output == "" {
		return nil
	}
	fmt.Println(output)
	return nil
}

func renderToolResult(result *mcptypes.CallToolResult) string {
	if result == nil {
		return ""
	}
	if result.StructuredContent != nil {
		data, err := json.Marshal(result.StructuredContent)
		if err == nil {
			return string(data)
		}
	}

	parts := make([]string, 0, len(result.Content))
	for _, content := range result.Content {
		if text, ok := content.(*mcptypes.TextContent); ok {
			parts = append(parts, text.Text)
			continue
		}

		data, err := json.Marshal(content)
		if err != nil {
			parts = append(parts, fmt.Sprintf("%v", content))
			continue
		}
		parts = append(parts, string(data))
	}

	return strings.Join(parts, "\n")
}
