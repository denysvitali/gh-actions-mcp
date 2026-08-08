# Architecture

## The whole thing in ten lines

1. `main.go` calls `cmd.Execute()`. Nothing else lives in the root package.
2. `cmd` parses flags, assembles a `*config.Config`, and picks a transport.
3. `config` layers defaults, a YAML file, and the environment into one value,
   then discovers a token (keychain, `gh auth token`) if none was given.
4. `mcp` owns the protocol: it registers 12 tools with typed input/output
   schemas and dispatches each call to a handler.
5. Handlers call `github`, which is the only package that speaks HTTP to
   GitHub, and which owns retries, the ETag cache, and log/zip parsing.
6. Results go back up as structured JSON; `cmd` renders them for a human when
   the CLI `tool` runner is used instead of a real MCP client.
7. There is exactly one `go` statement in the repository — the HTTP listener in
   `cmd/transport.go` — and its ownership contract is written at the statement.

## Dependency direction

```
main ──> cmd ──> mcp ──> github
                  │        (net/http, go-github, go-git)
                  └──> config
          └───────────────> github      (the logs and infer-repo commands)
          └───────────────> config
```

One-way and acyclic. Verified with `go list -deps`:

| Package | Depends on (in-module) |
|---|---|
| `config` | — |
| `github` | — |
| `mcp` | `config`, `github` |
| `cmd` | `config`, `github`, `mcp` |

`github` and `config` are leaves: neither knows that MCP or a CLI exists, which
is what makes them testable without a protocol session. Nothing imports `cmd`.

The rule to keep: **an arrow may never be added that points left.** If `github`
ever needs something from `mcp`, the thing belongs in `github` (or in a new leaf
package), not behind an import.

## Directory map

Snapshot of the tree as of 2026-07-25, after the file-splitting refactor.

```
main.go                       calls cmd.Execute; 9 lines

cmd/                          the CLI
  doc.go                      package doc, file map, flag-variable contract
  root.go                     cobra wiring: rootCmd, infer-repo, Execute, getVersion
  flags.go                    every flag variable + registerRootFlags/Logs/Tool
  config_load.go              config file + flags + git proxy detection -> *config.Config
  logs.go                     the `logs` command: resolve target, fetch, render
  tool.go                     the `tool` command: invoke one MCP tool from the CLI
  transport.go                stdio and Streamable HTTP transports

config/                       settings resolution (leaf)
  doc.go                      package doc, precedence, build tags
  config.go                   Config type, Load, defaults, env binding, file search
  validate.go                 Validate, ValidateToken, token discovery
  keychain.go                 !darwin stub
  keychain_darwin.go          darwin && cgo: the real keychain query
  keychain_darwin_nocgo.go    darwin && !cgo stub

github/                       the GitHub client (leaf)
  client.go                   Client, ClientOptions, constructors, TransportStats
  errors.go                   HTTPError, IsHTTPError
  types.go                    the DTOs
  runs.go workflows.go        runs and workflow listing/resolution
  logs.go log_filter.go       log fetching, zip reading, filtering
  log_sections.go             ##[group] section extraction
  artifacts.go                artifact listing, reading, atomic download
  checks.go                   check runs and combined status
  wait.go                     WaitForRun / WaitForAll / WaitForCommitChecks
  timing.go timing_stats.go   duration analysis
  diagnosis.go                failure diagnosis, flakiness, error extraction
  repo_detector.go            RepoDetector
  git_url.go url_parser.go    URL parsing (git remotes, Actions URLs)
  repo_infer.go git_local.go  owner/repo inference, local git queries
  git_remotes.go              remote enumeration
  gitconfig.go insteadof.go   git config reading, insteadOf rewrites
  cache.go retry.go           ETag cache and retry transports

mcp/                          the MCP server
  doc.go                      package doc
  server.go                   MCPServer, constructor, tool registration entry
  tools_*.go                  tool declarations, one file per family
  handlers_*.go               tool handlers, one file per family
  run_elements.go             the get_run element dispatch
  run_projections.go          run -> response shaping
  tool_inputs.go              the typed input/output structs
  sdk_adapter.go              the go-sdk tool/option builder
  invoke.go                   InvokeTool and the in-process session lifecycle
  results.go errors_format.go result and error formatting
  pagination.go               HMAC-protected opaque cursors
  resources.go                the workflow-run resource template
  typed_tools.go              remaining typed handlers

tests/                        build-tagged end-to-end tests
  main_test.go                goleak TestMain (untagged, so it always applies)
  integration_test.go         //go:build integration — needs GITHUB_TOKEN
  live_proxy_test.go          //go:build live — needs a reachable gh-proxy

docs/
  architecture.md             this file
  refactor-audit.md           the 2026-07 audit, 45 findings
  adr/                        accepted decisions
.golangci.yml                 the complexity budget, enforced in CI
```

## Where to put a new thing

| You are adding… | It goes in |
|---|---|
| a new MCP tool | `mcp/tools_<family>.go` + `mcp/handlers_<family>.go` |
| a new GitHub API call | the `github/` file named after the concern |
| a new CLI flag | `cmd/flags.go`, registered from the matching `register*Flags` |
| a new CLI subcommand | its own `cmd/<name>.go`, added in `root.go`'s `init` |
| a new config key | `config/config.go`: struct tag, `setDefaults`, `bindEnvVars`, and `config.yaml.example` |

## Invariants worth keeping

- **No file over 500 lines**, and a file over 300 needs a reason.
- **Cognitive complexity ≤ 15, functions ≤ 60 lines.** Enforced by
  `.golangci.yml` (`gocognit`, `gocyclo`, `funlen`).
- **No new mutex without a contract.** `forbidigo` rejects `sync.Mutex` and
  `sync.RWMutex` outright; a genuine lock needs `//nolint:forbidigo` and a
  one-line comment naming the fields it guards and the lock ordering.
- **Every `go` statement names its owner, its exit path, and its waiter.**
  There is one; keep it that way if you can.
- **No goroutine leaks.** `goleak.VerifyTestMain` runs in `cmd`, `config` and
  `tests`.
- **Exported API is a promise.** Packages live at the repository root, not under
  `internal/` (see `adr/0003-package-layout-not-internal.md`), so every exported
  identifier is potentially imported by someone else.
