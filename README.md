# GitHub Actions MCP Server

A Model Context Protocol (MCP) server for interacting with GitHub Actions. Provides tools to check workflow status, list workflows, and manage runs.

## Features

- **Get Actions Status**: View current status of GitHub Actions including recent workflow runs and statistics
- **List Workflows**: View all workflows available in the repository
- **Get Workflow Runs**: Get recent runs for a specific workflow
- **Trigger Workflow**: Manually trigger a workflow to run
- **Cancel Workflow Run**: Cancel a running workflow
- **Rerun Workflow**: Rerun a failed workflow

## Installation

```bash
# Build from source
make build

# Install
make install
```

## Configuration

### Authentication

The server requires a GitHub personal access token. Token sources (in order of precedence):

1. `--token` command line flag
2. `GITHUB_TOKEN` environment variable
3. `token` field in config file
4. macOS Keychain (automatic, if you've authenticated with `gh auth login`)

#### macOS Keychain Integration

On macOS, if no token is provided via the above methods, the server will automatically attempt to retrieve your GitHub token from the system keychain. This works seamlessly if you've previously authenticated using the GitHub CLI (`gh auth login`).

No additional configuration is required - just run `gh auth login` once and the token will be available to this MCP server.

### Config File

Create a `config.yaml` file:

```yaml
token: your_github_token  # Optional if using GITHUB_TOKEN env var or macOS keychain
repo_owner: your_username
repo_name: your_repo
log_level: info
```

Config file locations (in order of precedence):
1. `--config` flag
2. `./config.yaml`
3. `~/.config/gh-actions-mcp/config.yaml`
4. `/etc/gh-actions-mcp/config.yaml`

### Command Line Flags

```bash
gh-actions-mcp --repo-owner owner --repo-name repo --token ghp_xxxx
```

### Auto-detect Repository

If run from a git repository with an `origin` remote, the server will automatically infer the repository owner and name:

```bash
gh-actions-mcp infer-repo  # Shows inferred owner/repo
gh-actions-mcp --token $GITHUB_TOKEN  # Uses inferred values
```

## Usage

### Running as MCP Server

```bash
# Stdio mode (default, for Claude Desktop)
gh-actions-mcp --token $GITHUB_TOKEN

# SSE mode (for web-based MCP clients)
gh-actions-mcp --mcp-mode sse --mcp-port 8080

# HTTP mode
gh-actions-mcp --mcp-mode http --mcp-port 8080
```

### Claude Desktop Integration

Add to your `claude_desktop_config.json`:

```json
{
  "mcpServers": {
    "gh-actions": {
      "command": "/path/to/gh-actions-mcp",
      "args": ["--repo-owner", "your_owner", "--repo-name", "your_repo"],
      "env": {
        "GITHUB_TOKEN": "ghp_xxxx"
      }
    }
  }
}
```

**Note for macOS users:** If you've authenticated with `gh auth login`, you can omit the `env` block entirely - the token will be retrieved from your keychain automatically.

## Available Tools

### get_actions_status

Get the current status of GitHub Actions for the repository.

```json
{
  "name": "get_actions_status",
  "arguments": {
    "limit": 10
  }
}
```

### list_workflows

List all workflows available in the repository.

```json
{
  "name": "list_workflows",
  "arguments": {}
}
```

### get_workflow_runs

Get recent runs for a specific workflow.

```json
{
  "name": "get_workflow_runs",
  "arguments": {
    "workflow_id": "CI",
    "limit": 10
  }
}
```

### trigger_workflow

Trigger a workflow to run manually.

```json
{
  "name": "trigger_workflow",
  "arguments": {
    "workflow_id": "CI",
    "ref": "main"
  }
}
```

### cancel_workflow_run

Cancel a running workflow.

```json
{
  "name": "cancel_workflow_run",
  "arguments": {
    "run_id": 12345678
  }
}
```

### rerun_workflow

Rerun a failed workflow.

```json
{
  "name": "rerun_workflow",
  "arguments": {
    "run_id": 12345678
  }
}
```

## GitHub Token Permissions

Your GitHub personal access token needs the following permissions:

- `repo` - Full control of private repositories (to access workflow information)

### Why `repo` Scope is Required

The `repo` scope grants full read/write access to repository contents, including workflows. This is required because:

1. **Workflow Information**: GitHub Actions API requires full repository access to view workflow runs and statuses
2. **Workflow Management**: Triggering, canceling, and rerunning workflows requires write access to the repository
3. **Log Access**: Workflow logs are considered sensitive repository data

For public repositories, the `public_repo` scope may be sufficient for read-only operations, but `repo` is recommended for full functionality.

## API Rate Limit Handling

This tool uses the official GitHub Go library, which handles rate limiting automatically:

- **Authenticated requests**: 5,000 requests per hour
- **Unauthenticated requests**: 60 requests per hour

The library will automatically respect GitHub's rate limit headers and will return errors if the limit is exceeded. To avoid hitting rate limits:

- Use the `per_page_limit` configuration option to reduce the number of items fetched per request
- Cache results when making multiple calls in succession
- Use a valid GitHub token for higher rate limits

## Timeout Behavior for Workflows

The `wait_workflow_run` tool includes configurable timeout behavior:

- **Default timeout**: 600 seconds (10 minutes)
- **Default poll interval**: 5 seconds
- **Configuration**: Set via `timeout` and `poll_interval` parameters
- **No timeout**: Set `timeout` to 0 to wait indefinitely (not recommended)

Example:
```json
{
  "name": "wait_workflow_run",
  "arguments": {
    "run_id": 12345678,
    "poll_interval": 10,
    "timeout": 1800
  }
}
```

The tool will return a timeout error if the workflow doesn't complete within the specified time, along with the current status and elapsed time.

## Example Workflows

### Example 1: Check CI Status Before Deploying

```json
// Get the current status
{
  "name": "get_actions_status",
  "arguments": {
    "limit": 5
  }
}
```

### Example 2: Trigger and Wait for a Workflow

```json
// First, trigger the workflow
{
  "name": "trigger_workflow",
  "arguments": {
    "workflow_id": "CI",
    "ref": "main"
  }
}

// Then wait for it to complete (using the run_id from the trigger response)
{
  "name": "wait_workflow_run",
  "arguments": {
    "run_id": 12345678,
    "poll_interval": 5,
    "timeout": 600
  }
}
```

### Example 3: Get Filtered Logs

```json
// Get only lines containing "ERROR" with context
{
  "name": "get_workflow_logs",
  "arguments": {
    "run_id": 12345678,
    "filter": "ERROR",
    "context": 2
  }
}

// Get last 100 lines
{
  "name": "get_workflow_logs",
  "arguments": {
    "run_id": 12345678,
    "tail": 100
  }
}
```

### Example 4: List Recent Runs for a Workflow

```json
{
  "name": "get_workflow_runs",
  "arguments": {
    "workflow_id": "CI",
    "limit": 20
  }
}
```

## Advanced Configuration

### Environment Variables

The server supports both `GITHUB_*` and `GH_*` prefixed environment variables for backward compatibility:

| Config Field | GITHUB_* Prefix | GH_* Prefix | Description |
|--------------|-----------------|-------------|-------------|
| token | `GITHUB_TOKEN` | `GH_TOKEN` | GitHub personal access token |
| repo_owner | `GITHUB_REPO_OWNER` | `GH_REPO_OWNER` | Repository owner |
| repo_name | `GITHUB_REPO_NAME` | `GH_REPO_NAME` | Repository name |
| log_level | `GITHUB_LOG_LEVEL` | `GH_LOG_LEVEL` | Logging level (debug, info, warn, error) |
| default_limit | `GITHUB_DEFAULT_LIMIT` | `GH_DEFAULT_LIMIT` | Default list limit (default: 10) |
| default_log_len | `GITHUB_DEFAULT_LOG_LEN` | `GH_DEFAULT_LOG_LEN` | Default log line limit (default: 100) |
| per_page_limit | `GITHUB_PER_PAGE_LIMIT` | `GH_PER_PAGE_LIMIT` | API per-page limit (default: 50) |

The `GITHUB_*` prefixed variables take precedence over `GH_*` prefixed variables.

### Configuration File Options

Create a `config.yaml` file with any of these options:

```yaml
# Authentication
token: your_github_token  # Optional if using GITHUB_TOKEN env var or macOS keychain

# Repository
repo_owner: your_username
repo_name: your_repo

# Behavior
log_level: info                    # debug, info, warn, error
default_limit: 10                  # Default list limit
default_log_len: 100               # Default log line limit
per_page_limit: 50                 # GitHub API per-page limit (max 100)
```

## Keychain Setup Instructions (macOS)

On macOS, the server can automatically retrieve your GitHub token from the system keychain. This requires the GitHub CLI (`gh`) to be installed and configured.

### Setup Steps

1. **Install GitHub CLI**:
   ```bash
   brew install gh
   ```

2. **Authenticate**:
   ```bash
   gh auth login
   ```
   Follow the prompts to authenticate with your GitHub account.

3. **Verify Authentication**:
   ```bash
   gh auth status
   ```

Once authenticated, the MCP server will automatically use your stored credentials without requiring a `GITHUB_TOKEN` environment variable or config file entry.

### Keychain Benefits

- No need to store tokens in plain text config files
- Automatic credential management via `gh` CLI
- Shared credentials across multiple GitHub tools
- Secure storage using macOS keychain encryption

## Building and Development

### Build Tags

The project uses build tags to separate integration tests from unit tests:

- **Integration tests**: Require the `integration` build tag and network access to GitHub
- **Unit tests**: Run without build tags and don't require network access

Run all tests (including integration):
```bash
go test -tags=integration ./...
```

Run only unit tests:
```bash
go test ./...
```

### Building

```bash
# Build for your current platform
make build

# Build for specific platforms
make build-linux
make build-macos
make build-windows

# Install to $GOPATH/bin or $HOME/.local/bin
make install
```

## License

MIT
