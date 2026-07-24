# GitHub Actions MCP Server

A Model Context Protocol (MCP) server for interacting with GitHub Actions. It uses the official Go MCP SDK and provides tools to inspect, wait for, diagnose, and manage workflow runs.

## Features

- **List Workflows**: View all workflows available in the repository
- **Get Workflow Runs**: Get recent runs for a specific workflow
- **Analyze Timing**: Compare workflow, job, and step durations across recent runs to spot regressions and slow steps
- **Check Status**: View workflow status for a branch, tag, or commit
- **Manage Runs**: Cancel or rerun workflow runs
- **Diagnose Failure**: One-shot diagnosis of a failed run — identifies failed jobs/steps, extracts error lines from logs, and checks for flakiness

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
1. `--config` flag (explicit path)
2. `~/.config/gh-actions-mcp/config.yaml`
3. `/etc/gh-actions-mcp/config.yaml`

> **Note:** the working directory is intentionally NOT searched. When run from inside a project that happens to have a `config.yaml` of its own (e.g. another service's config), the global gh-actions-mcp config would otherwise be ignored. Use `--config` if you want to point at a project-local file.

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

### Git proxy auto-detection (gh-proxy)

If your git config rewrites GitHub URLs through a reverse proxy with
`url.<base>.insteadOf`, the server detects that and routes REST API calls
through the same proxy — no configuration needed:

```ini
# ~/.gitconfig
[url "http://user:password@gh-proxy.local/git/"]
	insteadOf = https://github.com/
```

From this the server derives `api_base_url = http://gh-proxy.local/api/` and
uses the embedded credentials as HTTP Basic auth (overriding any GitHub token,
which a proxy would not accept anyway). Detection also reverses the rewrite
when parsing remote URLs, so owner/repo inference works for proxied clones.

Detection is skipped when `api_base_url` is already configured. Disable it
with `--no-git-proxy-detect` or `git_proxy_detect: false` (env:
`GH_GIT_PROXY_DETECT=false`). Credentials are never logged; remote URLs
surfaced via MCP have passwords redacted.

## Usage

### Running as MCP Server

```bash
# MCP uses newline-delimited JSON over stdin/stdout (for Claude Desktop and other local clients)
gh-actions-mcp --token $GITHUB_TOKEN

# Or expose the MCP Streamable HTTP transport locally
gh-actions-mcp --token $GITHUB_TOKEN --transport http --http-address 127.0.0.1:8080
```

The Streamable HTTP endpoint defaults to `http://127.0.0.1:8080/mcp`, with a health check at `/healthz`. Non-loopback addresses require bearer authentication:

```bash
GH_ACTIONS_MCP_HTTP_TOKEN=change-me \
  gh-actions-mcp --transport http --http-address 0.0.0.0:8080
```

Clients must then send `Authorization: Bearer change-me`. Cross-origin and localhost protections from the official Go SDK remain enabled.

For direct production exposure, configure TLS and explicitly trusted browser origins:

```bash
GH_ACTIONS_MCP_HTTP_TOKEN=change-me \
  gh-actions-mcp --transport http --http-address 0.0.0.0:8443 \
  --http-tls-cert /run/secrets/tls.crt \
  --http-tls-key /run/secrets/tls.key \
  --http-allowed-origin https://mcp.example.com
```

Request bodies default to a 1 MiB limit (`--http-max-body`). The server also sets bounded read, write, idle, header, and graceful-shutdown timeouts. For OAuth deployments, terminate authentication and TLS in an API gateway and pass a dedicated bearer credential to this server; do not expose an unauthenticated non-loopback listener.

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

### get_check_status

Get workflow status for a branch, tag, or commit.

```json
{
  "name": "get_check_status",
  "arguments": {
    "ref": "main"
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

### list_runs

Get recent runs for a specific workflow.

```json
{
  "name": "list_runs",
  "arguments": {
    "workflow_id": 123456,
    "per_page": 10
  }
}
```

`list_runs` and `list_workflows` return an envelope containing `runs` or `workflows` plus an optional `next_cursor`. Pass that opaque value back as `cursor` to retrieve the next page. Cursors are versioned, HMAC-protected, and bound to the repository and active filters, so tampering and cross-query reuse are rejected. Conclusion-filtered run pages are filled across GitHub API pages instead of returning misleading sparse pages. These tools publish SDK-generated output schemas for their envelopes; stable JSON tools such as timing analysis, waits, run management, artifact access, and failure diagnosis also publish typed output schemas.

### analyze_timing

Compare the latest or a specific run against recent history, either at the workflow level or for a named job/step.

```json
{
  "name": "analyze_timing",
  "arguments": {
    "workflow": "CI",
    "limit": 10
  }
}
```

When `branch` is omitted, timing analysis uses recent runs across all branches.

To analyze a specific step within a job:

```json
{
  "name": "analyze_timing",
  "arguments": {
    "workflow": "CI",
    "job_name": "build",
    "step_name": "Unit Tests",
    "limit": 15
  }
}
```

### CLI Tool Runner

Invoke MCP tools locally from the CLI with a JSON argument object:

```bash
gh-actions-mcp tool list_runs --args '{"owner":"example-org","repo":"example-repo","workflow_id":123456,"per_page":10}'
gh-actions-mcp tool analyze_timing --args '{"owner":"example-org","repo":"example-repo","workflow":"CI","limit":10}'
```

### manage_run

Cancel or rerun an existing workflow run.

```json
{
  "name": "manage_run",
  "arguments": {
    "run_id": 12345678,
    "action": "rerun"
  }
}
```

### wait_all

Wait for every job in a workflow run to complete, regardless of whether jobs succeed, fail, or are cancelled. Unlike `wait_for_run`, this does not return early after a failed step.

```json
{
  "name": "wait_all",
  "arguments": {
    "run_id": 12345678,
    "timeout_minutes": 30
  }
}
```

### SDK resources

The server also exposes workflow-run metadata as an MCP resource template:

```text
github-actions://runs/{owner}/{repo}/{run_id}
```

For example, a client can read `github-actions://runs/octo-org/demo/12345678` and attach the returned `application/json` snapshot to a prompt. This is useful when the run metadata should be reused across several model turns without repeating a tool call.

Tool schemas use the official `modelcontextprotocol/go-sdk` validator. Invalid required arguments, enum values, and numeric limits are rejected before GitHub is contacted. SDK tool annotations also mark inspection tools as read-only and `manage_run`/`download_artifact` as potentially destructive, allowing capable hosts to present an appropriate confirmation step.

Safe GitHub API reads are retried up to three times for transport failures, HTTP 429, exhausted primary rate limits, and transient gateway/service failures. `Retry-After` and `X-RateLimit-Reset` are honored with a bounded delay, while exponential retries use jitter. Mutating requests are never replayed. Structured logs and in-process transport statistics expose latency, retry waits, rate-limit state, and ETag cache activity. Configure retries with `retry_max` or `GITHUB_RETRY_MAX`; set it to `-1` to disable retries.

`download_artifact` writes only beneath `artifact_root` (`GITHUB_ARTIFACT_ROOT`), uses a same-directory temporary file, and atomically publishes the completed artifact. Existing destinations are preserved unless `overwrite: true` is explicitly supplied.

Tool handlers are typed end-to-end through `modelcontextprotocol/go-sdk`; validated inputs are no longer converted back into untyped request maps. Build versions are reported consistently by `--version` and MCP initialization. Run transport-level protocol checks with:

```bash
make conformance
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

The `wait_for_run`, `wait_all`, and `wait_for_commit_checks` tools accept a `timeout_minutes` argument. The default is 30 minutes and the server caps individual waits at 120 minutes.

Example:
```json
{
  "name": "wait_for_run",
  "arguments": {
    "run_id": 12345678,
    "timeout_minutes": 30
  }
}
```

The tool will return a timeout error if the workflow doesn't complete within the specified time, along with the current status and elapsed time.

## Example Workflows

### Example 1: Check CI Status Before Deploying

```json
// Get the current status
{
  "name": "get_check_status",
  "arguments": {
    "limit": 5
  }
}
```

### Example 2: Trigger and Wait for a Workflow

```json
// First, obtain or trigger a run using the GitHub Actions workflow tooling.
{
  "name": "manage_run",
  "arguments": {
    "run_id": 12345678,
    "action": "rerun"
  }
}

// Then wait for it to complete (using the run_id from the trigger response)
{
  "name": "wait_for_run",
  "arguments": {
    "run_id": 12345678,
    "timeout_minutes": 30
  }
}
```

### Example 3: Get Filtered Logs

```json
// Get only lines containing "ERROR" with context
{
  "name": "get_run",
  "arguments": {
    "run_id": 12345678,
    "element": "logs",
    "search": "ERROR",
    "context": 2
  }
}

// Get last 100 lines
{
  "name": "get_run",
  "arguments": {
    "run_id": 12345678,
    "element": "logs",
    "tail": 100
  }
}
```

### Example 4: List Recent Runs for a Workflow

```json
{
  "name": "list_runs",
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
