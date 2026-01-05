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

## License

MIT
