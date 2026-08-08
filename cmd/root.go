package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/denysvitali/gh-actions-mcp/github"
	appmcp "github.com/denysvitali/gh-actions-mcp/mcp"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

var log = logrus.New()

func init() {
	log.SetFormatter(&logrus.TextFormatter{
		DisableTimestamp: false,
		FullTimestamp:    true,
	})

	registerRootFlags(rootCmd)
	registerLogsFlags(logsCmd)
	registerToolFlags(toolCmd)

	// Infer repo from git origin
	rootCmd.AddCommand(inferCmd)

	// Add logs command
	rootCmd.AddCommand(logsCmd)

	// Add generic tool command
	rootCmd.AddCommand(toolCmd)
}

var rootCmd = &cobra.Command{
	Use:   "gh-actions-mcp",
	Short: "MCP server for GitHub Actions",
	Long: `MCP server that provides tools for interacting with GitHub Actions.

This server can:
- Get GitHub Actions status and workflow runs
- List workflows
- Trigger, cancel, and rerun workflows

Token sources (in order of precedence):
1. --token flag
2. GITHUB_TOKEN environment variable
3. Config file token field
4. macOS Keychain (if authenticated via 'gh auth login')

Other configuration:
- Config file (--config or default locations)
- Command line flags (--repo-owner, --repo-name)
`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := configureLogLevel(); err != nil {
			return err
		}

		// Load config
		cfg, err := loadConfig()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		// Create MCP server
		mcpServer, err := appmcp.NewMCPServer(cfg, log)
		if err != nil {
			return err
		}
		defer func() { _ = mcpServer.Close() }()

		return serveMCP(cmd.Context(), mcpServer)
	},
}

var inferCmd = &cobra.Command{
	Use:   "infer-repo",
	Short: "Infer repository from git remote origin",
	Long:  "Get the repository owner and name from the git remote origin URL",
	RunE: func(cmd *cobra.Command, args []string) error {
		// Get git remote URL
		// noctx: cmd.Context() is nil until cobra's Execute sets it, so
		// exec.CommandContext would panic when RunE is called directly.
		// Wiring a context here would also start killing the git process on
		// cancellation, which is a behaviour change, not a refactor.
		cmdExec := exec.Command("git", "remote", "get-url", "origin") //nolint:noctx
		output, err := cmdExec.Output()
		if err != nil {
			return fmt.Errorf("failed to get git remote: %w (are you in a git repo with an 'origin' remote?)", err)
		}

		remoteURL := strings.TrimRight(string(output), "\n\r")

		owner, repo, err := github.InferRepoFromOrigin(remoteURL)
		if err != nil {
			return fmt.Errorf("failed to parse repo from URL: %w", err)
		}

		fmt.Printf("Owner: %s\n", owner)
		fmt.Printf("Repo:  %s\n", repo)
		fmt.Printf("\nYou can use these with:\n")
		fmt.Printf("  --repo-owner %s --repo-name %s\n", owner, repo)
		fmt.Printf("Or set in config:\n")
		fmt.Printf("  repo_owner: %s\n", owner)
		fmt.Printf("  repo_name: %s\n", repo)

		return nil
	},
}

func Execute() {
	// Add git info to version
	version = getVersion()
	rootCmd.Version = version

	if err := rootCmd.Execute(); err != nil {
		log.Fatal(err)
	}
}

func getVersion() string {
	// Try to get version from build
	buildInfo, ok := os.LookupEnv("VERSION")
	if ok {
		return buildInfo
	}
	if version != "" && version != "dev" {
		return version
	}

	// Try to get from git
	if dir, err := os.Getwd(); err == nil {
		gitDir := filepath.Join(dir, ".git")
		if _, statErr := os.Stat(gitDir); statErr == nil {
			// noctx: getVersion runs from Execute, before any command context
			// exists, and must not be cancellable.
			gitCmd := exec.Command("git", "describe", "--tags", "--always") //nolint:noctx
			if output, err := gitCmd.Output(); err == nil {
				return strings.TrimSpace(string(output))
			}
		}
	}

	return version
}
