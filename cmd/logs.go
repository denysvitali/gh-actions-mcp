package cmd

import (
	"context"
	"fmt"

	"github.com/denysvitali/gh-actions-mcp/config"
	"github.com/denysvitali/gh-actions-mcp/github"

	"github.com/spf13/cobra"
)

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
  gh-actions-mcp logs https://github.com/denysvitali/gh-actions-mcp/actions/runs/21662021288/job/62449039965

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

// logTarget is the repository and the run/job the `logs` command must read.
// A zero JobID means "the whole run".
type logTarget struct {
	Owner string
	Repo  string
	RunID int64
	JobID int64
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

	target, err := resolveLogTarget(args[0], cfg)
	if err != nil {
		return err
	}

	// Create GitHub client
	client, err := github.NewClientWithOptions(github.ClientOptions{
		Token:        cfg.Token,
		Owner:        target.Owner,
		Repo:         target.Repo,
		APIBaseURL:   cfg.APIBaseURL,
		UploadURL:    cfg.UploadURL,
		RetryMax:     cfg.RetryMax,
		AuthUsername: cfg.AuthUsername,
	})
	if err != nil {
		return fmt.Errorf("failed to create GitHub client: %w", err)
	}

	logs, err := fetchLogs(ctx, client, target, logFilterFromFlags())
	if err != nil {
		return describeLogsError(err, target)
	}

	// Output results
	if logs == "" {
		fmt.Println("(no matching logs)")
	} else {
		fmt.Print(logs)
	}

	return nil
}

// resolveLogTarget turns the single positional argument into a log target. It
// accepts a GitHub Actions run or job URL (which carries its own owner/repo),
// or a bare numeric run id, which is read against the configured repository
// and the --job-id flag. Anything else is rejected as an invalid run ID.
func resolveLogTarget(arg string, cfg *config.Config) (logTarget, error) {
	if github.IsActionsURL(arg) {
		parsed, err := github.ParseActionsURL(arg)
		if err != nil {
			return logTarget{}, fmt.Errorf("failed to parse URL: %w", err)
		}
		return logTarget{
			Owner: parsed.Owner,
			Repo:  parsed.Repo,
			RunID: parsed.RunID,
			JobID: parsed.JobID,
		}, nil
	}

	// Try to parse as run ID
	id, err := github.ParseRunID(arg)
	if err != nil {
		return logTarget{}, fmt.Errorf("invalid run ID: %w", err)
	}
	target := logTarget{
		Owner: cfg.RepoOwner,
		Repo:  cfg.RepoName,
		RunID: id,
		JobID: logsJobID,
	}

	// Validate we have owner and repo
	if target.Owner == "" || target.Repo == "" {
		return logTarget{}, fmt.Errorf("repository owner and name must be specified via URL, config, or --owner/--repo flags")
	}
	return target, nil
}

// logFilterFromFlags maps the --search/--regex/--context flags onto the
// filtering options understood by the github package.
func logFilterFromFlags() *github.LogFilterOptions {
	filterOpts := &github.LogFilterOptions{}
	if logsSearch != "" {
		filterOpts.Filter = logsSearch
	}
	if logsRegex != "" {
		filterOpts.FilterRegex = logsRegex
	}
	filterOpts.ContextLines = logsContext
	return filterOpts
}

// fetchLogs picks the endpoint that matches the target and the flags:
// --section extracts one group, a job id reads a single job, and otherwise the
// whole run is read.
func fetchLogs(
	ctx context.Context,
	client *github.Client,
	target logTarget,
	filterOpts *github.LogFilterOptions,
) (string, error) {
	switch {
	case logsSection != "":
		return client.GetLogSection(ctx, target.RunID, target.JobID, logsSection, filterOpts)
	case target.JobID > 0:
		return client.GetWorkflowJobLogs(ctx, target.JobID, logsHead, logsTail, logsOffset, logsNoHeaders, filterOpts)
	default:
		return client.GetWorkflowLogs(ctx, target.RunID, logsHead, logsTail, logsOffset, logsNoHeaders, filterOpts)
	}
}

// describeLogsError turns the two HTTP failures users actually hit into
// actionable messages naming the run and repository they used.
func describeLogsError(err error, target logTarget) error {
	if github.IsHTTPError(err, 404) {
		return fmt.Errorf("run or job not found (404). The run ID %d might not exist in %s/%s. Use the MCP tool list_repository_workflow_runs to find valid run IDs", target.RunID, target.Owner, target.Repo)
	}
	if github.IsHTTPError(err, 401) {
		return fmt.Errorf("authentication failed (401). Your token may not have access to %s/%s or the repository is private", target.Owner, target.Repo)
	}
	return fmt.Errorf("failed to get logs: %w", err)
}
