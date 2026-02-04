package cmd

import (
	"context"
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

// Logs command flags
var (
	logsSearch    string
	logsRegex     string
	logsSection   string
	logsContext   int
	logsTail      int
	logsHead      int
	logsOffset    int
	logsNoHeaders bool
	logsJobID     int64
	logsOwner     string
	logsRepo      string
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

	// Add logs command
	rootCmd.AddCommand(logsCmd)
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

var logsCmd = &cobra.Command{
	Use:   "logs [URL|run-id|job-id]",
	Short: "Fetch logs for a workflow run or job",
	Long: `Fetch and filter logs from GitHub Actions workflow runs or jobs.

CONCEPTS:
  Workflow    - A YAML file defining a CI/CD process (e.g., build.yml)
  Run         - A specific execution of a workflow (has a run ID)
  Job         - A step within a workflow run (has a job ID)
  Section     - A grouped portion of logs (marked by ##[group]...##[endgroup])

Supports GitHub Actions URLs:
  - Run URL: https://github.com/owner/repo/actions/runs/123456
  - Job URL: https://github.com/owner/repo/actions/runs/123456/job/789012

Examples:
  # Get all logs for a run
  gh-actions-mcp logs 21662021288

  # Get logs from a URL
  gh-actions-mcp logs https://github.com/denysvitali/gps-tracker-tr003-v2/actions/runs/21662021288/job/62449039965

  # Filter for specific text
  gh-actions-mcp logs 21662021288 --search "OTA task started"

  # Get specific section
  gh-actions-mcp logs 21662021288 --section "Flash and soak test"

  # Use regex filter
  gh-actions-mcp logs 21662021288 --regex "OTA.*started"

TIPS:
  - If you get a 404 error, the run ID might not exist. List runs using the MCP tool:
    list_workflow_runs or list_repository_workflow_runs
  - When using a URL, owner/repo are extracted from the URL automatically
  - Use --job-id to get logs for a specific job within a run
`,
	Args: cobra.ExactArgs(1),
	RunE: runLogs,
}

func init() {
	logsCmd.Flags().StringVarP(&logsSearch, "search", "s", "", "Filter lines containing substring")
	logsCmd.Flags().StringVar(&logsRegex, "regex", "", "Filter lines matching regex pattern")
	logsCmd.Flags().StringVar(&logsSection, "section", "", "Extract a specific section by name/pattern")
	logsCmd.Flags().IntVarP(&logsContext, "context", "C", 0, "Show N lines of context around matches")
	logsCmd.Flags().IntVar(&logsTail, "tail", 0, "Show last N lines")
	logsCmd.Flags().IntVar(&logsHead, "head", 0, "Show first N lines")
	logsCmd.Flags().IntVar(&logsOffset, "offset", 0, "Skip first N lines")
	logsCmd.Flags().BoolVar(&logsNoHeaders, "no-headers", false, "Don't print file headers")
	logsCmd.Flags().Int64VarP(&logsJobID, "job-id", "j", 0, "Specific job ID (when using run ID)")
	logsCmd.Flags().StringVar(&logsOwner, "owner", "", "Override repo owner")
	logsCmd.Flags().StringVar(&logsRepo, "repo", "", "Override repo name")
}

func runLogs(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	// Load config
	cfg, err := loadConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Override with CLI flags
	if logsOwner != "" {
		cfg.RepoOwner = logsOwner
	}
	if logsRepo != "" {
		cfg.RepoName = logsRepo
	}

	// Parse the argument (URL or ID)
	arg := args[0]

	var owner, repo string
	var runID, jobID int64

	// Check if it's a URL
	if github.IsActionsURL(arg) {
		parsed, err := github.ParseActionsURL(arg)
		if err != nil {
			return fmt.Errorf("failed to parse URL: %w", err)
		}
		owner = parsed.Owner
		repo = parsed.Repo
		runID = parsed.RunID
		jobID = parsed.JobID
	} else {
		// Try to parse as run ID
		id, err := github.ParseRunID(arg)
		if err != nil {
			return fmt.Errorf("invalid run ID: %w", err)
		}
		runID = id
		jobID = logsJobID
		owner = cfg.RepoOwner
		repo = cfg.RepoName
	}

	// Validate we have owner and repo
	if owner == "" || repo == "" {
		return fmt.Errorf("repository owner and name must be specified via URL, config, or --owner/--repo flags")
	}

	// Create GitHub client
	client := github.NewClient(cfg.Token, owner, repo)

	// Prepare filter options
	filterOpts := &github.LogFilterOptions{}
	if logsSearch != "" {
		filterOpts.Filter = logsSearch
	}
	if logsRegex != "" {
		filterOpts.FilterRegex = logsRegex
	}
	filterOpts.ContextLines = logsContext

	// Fetch logs
	var logs string

	if logsSection != "" {
		// Extract specific section
		logs, err = client.GetLogSection(ctx, runID, jobID, logsSection, filterOpts)
	} else if jobID > 0 {
		// Get logs for specific job
		logs, err = client.GetWorkflowJobLogs(ctx, jobID, logsHead, logsTail, logsOffset, logsNoHeaders, filterOpts)
	} else {
		// Get logs for run
		logs, err = client.GetWorkflowLogs(ctx, runID, logsHead, logsTail, logsOffset, logsNoHeaders, filterOpts)
	}

	if err != nil {
		// Provide helpful error messages for common HTTP errors
		if github.IsHTTPError(err, 404) {
			return fmt.Errorf("run or job not found (404). The run ID %d might not exist in %s/%s. Use the MCP tool list_repository_workflow_runs to find valid run IDs", runID, owner, repo)
		}
		if github.IsHTTPError(err, 401) {
			return fmt.Errorf("authentication failed (401). Your token may not have access to %s/%s or the repository is private", owner, repo)
		}
		return fmt.Errorf("failed to get logs: %w", err)
	}

	// Output results
	if logs == "" {
		fmt.Println("(no matching logs)")
	} else {
		fmt.Print(logs)
	}

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
