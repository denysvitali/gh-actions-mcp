package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/denysvitali/gh-actions-mcp/config"
	"github.com/denysvitali/gh-actions-mcp/github"
	"github.com/denysvitali/gh-actions-mcp/mcp"

	"github.com/mark3labs/mcp-go/server"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

var (
	version   = "dev"
	cfgFile   string
	repoOwner string
	repoName  string
	token     string
	logLevel  string
)

var log = logrus.New()

func init() {
	log.SetFormatter(&logrus.TextFormatter{
		DisableTimestamp: false,
		FullTimestamp:    true,
	})

	rootCmd.PersistentFlags().StringVarP(&cfgFile, "config", "c", "", "config file path")
	rootCmd.PersistentFlags().StringVarP(&repoOwner, "repo-owner", "o", "", "repository owner")
	rootCmd.PersistentFlags().StringVarP(&repoName, "repo-name", "r", "", "repository name")
	rootCmd.PersistentFlags().StringVarP(&token, "token", "t", "", "GitHub token (or use GITHUB_TOKEN env var, or macOS keychain)")
	rootCmd.PersistentFlags().StringVarP(&logLevel, "log-level", "l", "info", "log level (debug, info, warn, error)")

	// Infer repo from git origin
	rootCmd.AddCommand(inferCmd)
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
		// Set log level
		level, err := logrus.ParseLevel(logLevel)
		if err != nil {
			return fmt.Errorf("invalid log level: %w", err)
		}
		log.SetLevel(level)

		// Load config
		cfg, err := loadConfig()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		// Create MCP server
		mcpServer := mcp.NewMCPServer(cfg, log)

		// Run stdio transport using the library's built-in handler
		return server.ServeStdio(mcpServer.GetServer())
	},
}

var inferCmd = &cobra.Command{
	Use:   "infer-repo",
	Short: "Infer repository from git remote origin",
	Long:  "Get the repository owner and name from the git remote origin URL",
	RunE: func(cmd *cobra.Command, args []string) error {
		// Get git remote URL
		cmdExec := exec.Command("git", "remote", "get-url", "origin")
		output, err := cmdExec.Output()
		if err != nil {
			return fmt.Errorf("failed to get git remote: %v (are you in a git repo with an 'origin' remote?)", err)
		}

		remoteURL := string(output)
		remoteURL = remoteURL[:len(remoteURL)-1] // Remove trailing newline

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

func loadConfig() (*config.Config, error) {
	config.SetLogger(log)

	cfg, err := config.Load(cfgFile)
	if err != nil {
		return nil, err
	}

	// Override with CLI flags
	if repoOwner != "" {
		cfg.RepoOwner = repoOwner
	}
	if repoName != "" {
		cfg.RepoName = repoName
	}
	if token != "" {
		cfg.Token = token
	}
	if logLevel != "" {
		cfg.LogLevel = logLevel
	}

	// Try to infer repo from git if not set
	if cfg.RepoOwner == "" || cfg.RepoName == "" {
		if inferErr := inferRepoFromGit(cfg); inferErr != nil {
			log.Warnf("Could not infer repo from git: %v", inferErr)
		}
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	log.Infof("Configured for repository: %s/%s", cfg.RepoOwner, cfg.RepoName)
	return cfg, nil
}

func inferRepoFromGit(cfg *config.Config) error {
	cmd := exec.Command("git", "remote", "get-url", "origin")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("git command failed: %w", err)
	}

	remoteURL := string(output)
	remoteURL = remoteURL[:len(remoteURL)-1]

	owner, repo, err := github.InferRepoFromOrigin(remoteURL)
	if err != nil {
		return err
	}

	if cfg.RepoOwner == "" {
		cfg.RepoOwner = owner
	}
	if cfg.RepoName == "" {
		cfg.RepoName = repo
	}

	log.Infof("Inferred repository from git: %s/%s", owner, repo)
	return nil
}

func Execute() {
	// Add git info to version
	version = getVersion()

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

	// Try to get from git
	if dir, err := os.Getwd(); err == nil {
		gitDir := filepath.Join(dir, ".git")
		if _, statErr := os.Stat(gitDir); statErr == nil {
			gitCmd := exec.Command("git", "describe", "--tags", "--always")
			if output, err := gitCmd.Output(); err == nil {
				return string(output[:len(output)-1])
			}
		}
	}

	return version
}
