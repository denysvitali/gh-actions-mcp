# Refactor audit — gh-actions-mcp

Date: 2026-07-24
Commit: `311ac60` plus uncommitted work in `README.md`, `mcp/sdk_adapter.go`, `mcp/server.go`,
`mcp/server_test.go`, `mcp/typed_tools.go`.

No code was edited to produce this document.

---

## 1. Metrics baseline

| Metric | Value |
|---|---|
| Go LOC (all `*.go`) | 13,536 |
| Go files | 36 (17 non-test, 19 test) |
| Packages | `main`, `cmd`, `config`, `github`, `mcp`, `tests` |
| `go build ./...` | green |
| `go vet ./...` | green |
| `go test ./...` | green |
| `go test -race ./...` | green |
| golangci-lint (`--default=standard`, no config) | 37 issues: 31 `errcheck` (all in `_test.go`), 6 `staticcheck` |
| `.golangci.yml` | **absent** |
| `docs/` | **absent** (this file is the first) |
| Coverage | `cmd` 46.7%, `config` 80.4%, `github` 64.6%, `mcp` 46.7%, root `main` 0.0% |

### Longest files (threshold: justify >300, must fix >500)

| File | Lines | Verdict |
|---|---|---|
| `github/client.go` | 3,470 | **must fix** — 7× the limit |
| `github/client_test.go` | 2,057 | **must fix** |
| `mcp/server.go` | 722 | **must fix** |
| `mcp/server_test.go` | 691 | **must fix** |
| `cmd/root.go` | 588 | **must fix** |
| `mcp/typed_tools.go` | 541 | **must fix** |
| `github/repo_detector.go` | 529 | **must fix** |
| `config/config_test.go` | 513 | **must fix** |
| `tests/integration_test.go` | 478 | justify |
| `github/gitconfig_test.go` | 444 | justify |
| `mcp/sdk_adapter.go` | 327 | justify |

### Exported identifiers per package (threshold: justify >20, must fix >40)

| Package | Exported-ish decls | Non-test files | Verdict |
|---|---|---|---|
| `github` | 227 | 7 | **must fix** — 5.7× the limit |
| `mcp` | 44 | 5 | **must fix** (marginal) |
| `config` | 24 | 4 | justify |
| `cmd` | 18 | 2 | ok |

`github` is not a package, it is a monolith. 227 exported identifiers in 7 files means a
newcomer cannot guess where anything lives, and any change to one concern is a diff against
a 3,470-line file.

### Cognitive complexity (`gocognit`, non-test; justify >10, must fix >15)

40 functions over 10. Top 20:

| Cognitive | Function | Location |
|---|---|---|
| 68 | `(*Client).waitForRun` | `github/client.go:2628` |
| 46 | `(*Client).AnalyzeTiming` | `github/client.go:1495` |
| 44 | `filterLogLines` | `github/client.go:665` |
| 41 | `(*Client).DownloadArtifactWithOptions` | `github/client.go:2298` |
| 39 | `(*MCPServer).getRunTyped` | `mcp/typed_tools.go:212` |
| 38 | `(*Client).GetCheckRunsForRef` | `github/client.go:2472` |
| 36 | `(*Client).WaitForCommitChecks` | `github/client.go:2786` |
| 33 | `(*Client).extractErrorLines` | `github/client.go:3317` |
| 32 | `(*Client).GetArtifactContent` | `github/client.go:2154` |
| 27 | `ParseGitURL` | `github/repo_detector.go:50` |
| 25 | `extractSection` | `github/client.go:2999` |
| 25 | `buildStepBreakdown` | `github/client.go:1907` |
| 24 | `runLogs` | `cmd/root.go:444` |
| 24 | `readZipArchive` | `github/client.go:924` |
| 24 | `(*Client).checkFlakiness` | `github/client.go:3369` |
| 23 | `(*Client).GetWorkflowJobLogs` | `github/client.go:1986` |
| 23 | `(*RetryTransport).RoundTrip` | `github/retry.go:69` |
| 22 | `formatLogFiles` | `github/client.go:1038` |
| 20 | `(*Client).DiagnoseFailure` | `github/client.go:3183` |
| 19 | `loadConfigWithOptions` | `cmd/root.go:172` |

Cyclomatic (`gocyclo`) tops out at 35 (`DownloadArtifactWithOptions`); 40 functions are over 8.

### Functions over 60 lines (non-test)

`(*MCPServer).registerTools` **361 lines** (`mcp/server.go:286`) is the single worst offender —
6× the limit, one flat wall of tool declarations.

Others: `waitForRun` 158, `AnalyzeTiming` 148, `DownloadArtifactWithOptions` 139,
`GetArtifactContent` 131, `readZipArchive` 114, `runLogs` 112, `GetCheckRunsForRef` 110,
`WaitForCommitChecks` 104, `cmd.init()` 104, `getRunTyped` 96, `config.Load` 91,
`filterLogLines` 91, `DiagnoseFailure` 86, `ParseGitURL` 83, `(*RepoDetector).Detect` 83,
`GetWorkflowJobLogs` 76, `NewClientWithOptions` 75, `checkFlakiness` 72,
`ListRepositoryWorkflowRunsPage` 71, `extractSectionName` 67, `extractSection` 64,
`loadConfigWithOptions` 63.

### `dupl -threshold 60`

12 clone groups. Four are in non-test code:

| Clones | Assessment |
|---|---|
| `github/client.go:1267` ↔ `github/repo_detector.go:73` | **true duplication** — `InferRepoFromOrigin` is a fork of `ParseGitURL` |
| `github/repo_detector.go:86` ↔ `:108` | **structural boilerplate** — HTTPS vs `git://` branches of `ParseGitURL` |
| `mcp/server.go:495↔517`, `:539↔604` | **structural boilerplate** — repeated tool declaration blocks in `registerTools` |
| `mcp/typed_tools.go:204↔300`, `:206↔303` | one-line clones — noise, ignore |

Test-side clones (`cmd/root_test.go` ×3, `github/client_test.go` ×3, `github/insteadof_test.go`,
`mcp/server_test.go`, `tests/integration_test.go`) are table-driven-test candidates.

### Concurrency inventory

| Primitive | Location | Guards / owns |
|---|---|---|
| `sync.RWMutex` (package global) | `github/client.go:88` `regexCacheMutex` | global `regexCache map[string]*regexp.Regexp` |
| `sync.Mutex` | `github/cache.go:15` | `ETagCacheTransport.entries` |
| `sync.RWMutex` | `github/repo_detector.go:38` | `RepoDetector.cache` |
| `sync.Mutex` | `mcp/server.go:27` `invokeMu` | `invokeSession`, `invokeServerSession`, `invokeCancel` |
| `atomic.Int64` ×10 | `github/retry.go:32-41` | counters — correct as-is |
| `atomic.Int64` ×3 | `github/cache.go:17` | counters — correct as-is |
| bare `go func` | `cmd/transport.go:87` | HTTP `ListenAndServe`; exit via `errCh`, owner is `serveStreamableHTTP` — **acceptable, undocumented** |
| `sync.WaitGroup` | none | — |

No lock has a doc comment naming the fields it guards. No `goleak` check exists.

---

## 2. Findings

Severity: **H** = must fix, **M** = should fix, **L** = nice to have.
Category: cx = complexity, dup = duplication, doc = documentation, fg = footgun,
conc = concurrency, str = structure.

| # | Location | Issue | Cat | Sev | Proposed action | Risk | Size |
|---|---|---|---|---|---|---|---|
| 1 | `github/client.go` (whole) | 3,470-line god file holding HTTP errors, client construction, ~30 DTOs, git helpers, log filtering, zip reading, timing analysis, artifacts, check runs, waiting, log sections and failure diagnosis | str | H | Split into ~12 files by concern, `git mv`-style moves with no content edits | low (moves only) | L |
| 2 | `mcp/server.go:286` | `registerTools` is 361 lines of flat tool declarations | cx | H | Split into one `register<Domain>Tools` per tool family, across `server_tools_*.go` | low | M |
| 3 | `github/client.go:2628` | `waitForRun` cognitive 68 / 158 lines / `waitAll bool` parameter | cx, fg | H | Extract poll-decision as pure function; replace `bool` with a named option type | medium | M |
| 4 | `github/client.go:1495` | `AnalyzeTiming` cognitive 46 / 148 lines | cx | H | Extract sample collection, breakdown build, comparison into named steps (helpers already exist) | medium | M |
| 5 | `github/client.go:2298` | `DownloadArtifactWithOptions` cyclomatic 35 / 139 lines | cx | H | Split fetch / validate destination / write-atomically | medium | M |
| 6 | `github/client.go:1126,1170,1986,2062` | 8-, 7-, 7-, 8-parameter log functions with three interchangeable `int`s (`head, tail, offset`) plus `noHeaders bool` | fg | H | Introduce `LogViewOptions{Head, Tail, Offset int; NoHeaders bool; Filter *LogFilterOptions}`; keep old exported signatures as thin wrappers so no API breaks | medium | M |
| 7 | `github/client.go:45` | `IsHTTPError` uses a bare type assertion, so a wrapped `*HTTPError` is missed and it falls through to regex-matching the message string | fg | H | **Bug — see `FINDINGS.md#1`.** Refactor may not change behaviour; fix in a separate `fix:` commit | — | S |
| 8 | `github/client.go:55` | `regexp.MustCompile` compiled on **every** `IsHTTPError` call | cx | M | Hoist to a package-level `var` | low | S |
| 9 | `github/client.go:1249` vs `github/repo_detector.go:50` | `InferRepoFromOrigin` is a fork of `ParseGitURL`: identical bare and SSH branches, **different** HTTPS branch (no host validation, no `git://`, no token check) | dup | H | Extract the identical bare+SSH branches into one unexported helper; leave the HTTPS divergence in place and annotate it — merging them would change behaviour (see `FINDINGS.md#2`) | medium | M |
| 10 | `github/client.go:86-89` | Package-global `regexCache` map + `RWMutex`, unbounded: every distinct user-supplied filter pattern is cached forever | conc, fg | H | Move the cache onto `Client` (or bound it); document the lock's guarded fields | medium | M |
| 11 | `github/client.go:28-32` | `var log = logrus.New()` + `SetLogger` — mutable package global written after goroutines may read it | conc, fg | M | Document as init-only, or thread the logger through `ClientOptions` | medium | M |
| 12 | `github/client.go:92` | `presignedHTTPClient` package global; the 30s timeout has no justification comment | doc | L | Comment the value, or move onto `Client` | low | S |
| 13 | `github/client.go:108` | `NewClientWithPerPage` discards `NewClientWithOptions`'s error (`c, _ :=`) and can return a nil `*Client` | fg | M | Silent failure path — keep the signature (public API) but log, and document that a nil return is possible | low | S |
| 14 | `github/cache.go:13-18` | Lock has no doc comment naming guarded fields; `NewETagCacheTransport` and `Stats`/`ETagCacheStats` have no doc comments | conc, doc | M | Add §7.2-style concurrency contract; the mutex is justified (short, non-blocking critical sections) — record that | low | S |
| 15 | `github/repo_detector.go:37-40` | `RepoDetector.mu` guards a single `*RepoInfo` pointer; read-mostly, written once | conc | M | `atomic.Pointer[RepoInfo]` — removes the lock entirely | low | S |
| 16 | `github/repo_detector.go:50` | `ParseGitURL` 83 lines / cognitive 27; HTTPS and `git://` branches are near-identical | cx, dup | M | Table of scheme handlers, or extract `parseHTTPStyleURL` used by both | low | M |
| 17 | `github/repo_detector.go:201` | `(*RepoDetector).Detect` 83 lines | cx | M | Extract remote-selection and URL-resolution steps | medium | M |
| 18 | `github/repo_detector.go:20-25` | Second mutable logger global (`detectorLog` + `SetDetectorLogger`) — two loggers in one package | str, fg | M | Collapse onto the package logger | low | S |
| 19 | `github/section_test.go` | Tests `extractSection`/`extractSections`, which live in `client.go` — no `section.go` exists | str | M | Covered by finding 1: create `github/log_sections.go` | low | S |
| 20 | `mcp/server.go:20-31` | `MCPServer` mixes 5 service fields with 4 lazy-session fields; `invokeMu` has no contract comment | conc, cx | M | Extract the session quartet into an `invokeSession` struct with its own documented contract | low | M |
| 21 | `mcp/server.go:676` | `context.Background()` created inside `invokeClientSession`, below `main` | fg | M | Accept a `ctx` from the caller, or document why the session must outlive the request | medium | S |
| 22 | `mcp/server.go:121` | `formatAuthErrorWithRepo` classifies errors by `strings.Contains` on lower-cased message text (`"401"`, `"403"`, `"404"`, …) | fg | M | Prefer `errors.As`/`*ghapi.ErrorResponse` status codes where available; keep string fallback last | medium | M |
| 23 | `mcp/typed_tools.go:212` | `getRunTyped` cognitive 39 / 96 lines — a 7-way `element` dispatch | cx | H | Map of `element` → handler func, one small function each | low | M |
| 24 | `mcp/server.go:49` | `isValidRunElement` linear scan of `validRunElements`; the element set is duplicated between validation, the tool schema enum and `getRunTyped`'s switch | dup | M | Single source of truth: one map from element name to handler, derive the enum from it | low | M |
| 25 | `mcp/server.go:165,177` | `jsonResult`/`jsonResultPretty` take `interface{}` | fg | L | Generics or a concrete result type | low | S |
| 26 | `mcp/sdk_adapter.go:10-129` | `toolBuilder` is a stateless empty struct used as a namespace; `toolOption func(any)` type-switches on `any` at runtime, so a misapplied option fails silently at runtime instead of at compile time | fg | M | Split `toolOption` into tool-level and property-level types so misuse cannot compile | medium | M |
| 27 | `mcp/*` | No `doc.go`; package has 5 non-test files and 44 exported identifiers | doc | M | Add `doc.go` stating what belongs here and the concurrency model | low | S |
| 28 | `github/*` | No `doc.go` (7 non-test files) | doc | M | Add `doc.go` | low | S |
| 29 | `cmd/root.go:60` | `init()` 104 lines registering ~30 flags into package-level `var`s | fg, cx | M | Extract `registerRootFlags(cmd *cobra.Command)` called from `init`; flags stay package-level (cobra idiom) | low | M |
| 30 | `cmd/root.go:444` | `runLogs` 112 lines / cognitive 24 | cx | H | Split argument resolution (URL vs run ID vs job ID) from fetching and rendering | medium | M |
| 31 | `cmd/root.go:172` | `loadConfigWithOptions(requireRepo bool)` — boolean parameter, 63 lines | fg, cx | M | Two named functions over one shared core (`loadConfig`/`loadConfigAllowMissingRepo` already exist as wrappers — invert so the bool disappears) | low | S |
| 32 | `cmd/root.go:22-58` | ~30 mutable package-level flag vars plus a third `log` global | fg | M | Accept as cobra idiom; document. Do not restructure — high churn, low payoff | — | — |
| 33 | `cmd/transport.go:87` | `go func` has an exit path (`errCh`) but no owner/lifecycle comment | conc, doc | M | Add the §7.2 comment; behaviour unchanged | low | S |
| 34 | `cmd/transport.go:73` | `WriteTimeout: 125 * time.Minute` — an unexplained magic value | doc | M | Comment why (presumably `wait_*` tools' max timeout + margin) or delete the claim | low | S |
| 35 | `config/config.go:59` | `Load` 91 lines / cognitive 16 | cx | M | Extract viper setup, path search, and env binding | low | M |
| 36 | `config/keychain.go` + `keychain_darwin*.go` | Three files for one build-tagged concern, no `doc.go`, no comment explaining the `nocgo` variant | doc, str | L | Add doc comments naming the build tags | low | S |
| 37 | repo root | No `.golangci.yml`; CI cannot catch regressions of anything in this table | str | H | Add the Phase-C config | low | M |
| 38 | repo root | 31 unchecked `Close()` calls in tests (`errcheck`) | fg | M | Fix so `errcheck` can be enabled repo-wide | low | M |
| 39 | `github/client.go:1046,3445,3456` | `WriteString(fmt.Sprintf(...))` (staticcheck QF1012) ×3 | cx | L | `fmt.Fprintf` | low | S |
| 40 | `github/client.go:2593` | `if` chain that should be a tagged switch on `cr.Conclusion` (QF1003) | cx | L | Tagged switch | low | S |
| 41 | `github/client.go:1398,1400` | Redundant embedded-field selectors (QF1008) ×2 | cx | L | Drop `.ListOptions` | low | S |
| 42 | root package | `main.go` 9 lines, 0% coverage — correct and fine | — | — | No action | — | — |
| 43 | `tests/` | Package named `tests` — a layer name, not a domain; holds integration + live-proxy tests | str | L | Leave. Renaming would break nothing but gains little; note as accepted deviation | — | — |
| 44 | `github/client_test.go` (2,057), `mcp/server_test.go` (691), `config/config_test.go` (513) | Test files over 500 lines, with `dupl` clone groups inside | cx, dup | M | Split alongside the source split (finding 1) and collapse clones into table-driven subtests | low | L |
| 45 | repo-wide | No `goleak` check in any `TestMain` | conc | M | Add to `github`, `mcp`, `cmd` | low | S |

### Doc drift list

Every command and claim in `README.md` must be executed. Candidates checked so far
(to be confirmed during Phase B):

| README claim | Status |
|---|---|
| `--transport http --http-address`, `--http-token`, `--http-tls-cert/key`, `--http-allowed-origin`, `--http-max-body`, `--http-path` | flags exist in `cmd/root.go` / `cmd/transport.go` — **verify each by running `--help`** |
| `GH_ACTIONS_MCP_HTTP_TOKEN` env var | exists (`cmd/transport.go:128`) |
| `--no-git-proxy-detect` / `git_proxy_detect: false` | verify both spellings resolve |
| "Request bodies default to a 1 MiB limit" | verify the default value of `mcpHTTPMaxBody` |
| "the working directory is intentionally NOT searched" for `config.yaml` | verify against `config.Load` |
| All JSON tool-argument examples under "Available Tools" | verify each against the registered schemas — **untested prose, highest drift risk** |
| CLI examples (`gh-actions-mcp tool list_runs --args '…'`) | verify by running |
| `config.yaml.example` keys | verify each key is read by `config.Load` |
| `install-codex.sh` | verify it still matches the build/install steps in the README |
| `Makefile` targets referenced by the README | verify |

No `docs/` directory existed before this audit, so there is no stale architecture doc to correct.

### Proposed target tree

Behaviour-preserving. Package import paths are **unchanged** — no `internal/` move, because
`github`, `mcp`, `config` and `cmd` are already domain-shaped and moving them would be a
`BREAKING:` change for any external importer for zero reader benefit. The win here is
**intra-package file splitting**.

```
  main.go                                  (unchanged)
  cmd/
+   doc.go                                 package doc
    root.go                                cobra wiring only, <150 lines
+   flags.go                               flag registration (from init())
+   config_load.go                         loadConfig* + git proxy detection
+   logs.go                                logsCmd + runLogs
+   tool.go                                toolCmd + runTool + renderToolResult
    transport.go                           (unchanged content, + goroutine contract comment)
  config/
+   doc.go
    config.go                              Config type + Load
+   validate.go                            Validate / ValidateToken
    keychain*.go                           (unchanged)
  github/
+   doc.go                                 package doc + concurrency model
    client.go                              Client, ClientOptions, constructors, TransportStats
+   errors.go                              HTTPError, IsHTTPError, newHTTPErrorFromGitHub
+   types.go                               WorkflowRun, Workflow, Job, Step, … DTOs
+   git_local.go                           GetCurrentBranch, GetLastCommit, CommitInfo
+   repo_infer.go                           InferRepoFromOrigin + shared URL helper
+   runs.go                                GetActionsStatus, GetWorkflowRun(s), List*, Resolve*
+   workflows.go                           GetWorkflows, ListWorkflowsPage, TriggerWorkflow
+   logs.go                                log filtering, zip reading, formatting, LogViewOptions
+   log_sections.go                        extractSection(s), ListLogSections, GetLogSection
+   artifacts.go                           Artifact*, GetArtifactContent, Download*
+   checks.go                              CheckRun, CombinedCheckStatus, GetCheckRunsForRef
+   wait.go                                WaitForRun, WaitForAll, WaitForCommitChecks, ManageRun
+   timing.go                              AnalyzeTiming + all timing helpers
+   diagnosis.go                           DiagnoseFailure, flakiness, error extraction
    repo_detector.go                       RepoDetector only
+   git_url.go                             ParseGitURL + scheme handlers (from repo_detector.go)
    url_parser.go                          ParseActionsURL (unchanged)
    cache.go retry.go gitconfig.go insteadof.go   (unchanged content, + doc comments)
  mcp/
+   doc.go
    server.go                              MCPServer type, constructor, GetServer, <200 lines
+   invoke.go                              InvokeTool, session lifecycle, Close
+   results.go                             jsonResult, textResult, errorResult, truncateLogResult
+   errors_format.go                       formatAuthError*
+   tools_runs.go                          registerRunTools (from registerTools)
+   tools_logs.go                          registerLogTools
+   tools_artifacts.go                     registerArtifactTools
+   tools_wait.go                          registerWaitTools
    sdk_adapter.go                         toolBuilder + options
+   tool_inputs.go                         all *Input / *Output structs (from sdk_adapter.go)
    typed_tools.go                         handlers, split per family if still >300 lines
    resources.go pagination.go             (unchanged content, + doc comments)
  docs/
+   refactor-audit.md                      (this file)
+   refactor-report.md                     Phase-C deliverable
+   architecture.md
+   adr/0001-etag-cache-mutex.md
+   adr/0002-repo-detector-atomic-pointer.md
+   adr/0003-package-layout-not-internal.md
+ .golangci.yml
+ FINDINGS.md                              bugs found, not fixed
```

Test files move with their source (`github/logs_test.go`, `github/timing_test.go`, …),
splitting `github/client_test.go` (2,057 lines) and `mcp/server_test.go` (691 lines).

---

## 3. Ordered plan

Three agents, partitioned **by package** so no two ever touch the same file. Every agent
keeps the exported API of its package byte-identical, so the other two keep compiling.

**Agent A — package `github`** (findings 1, 3–6, 8–19, 28, 39–41, 44 for `client_test.go`)
1. `test:` characterization tests for `waitForRun`, `AnalyzeTiming`, `DownloadArtifactWithOptions`,
   `filterLogLines`, `InferRepoFromOrigin`, `ParseGitURL` (pin current behaviour, including the
   two bugs in `FINDINGS.md` — the tests document, not fix, them)
2. `refactor:` file split of `client.go` and `repo_detector.go` — **moves only**
3. `refactor:` complexity reductions, one theme per commit
4. `refactor:` `RepoDetector.mu` → `atomic.Pointer`; document `ETagCacheTransport.mu`
5. `docs:` `doc.go`, exported doc comments, ADRs 0001/0002

**Agent B — package `mcp`** (findings 2, 20–27, 44 for `server_test.go`)
1. `test:` characterization tests for `getRunTyped`'s 7 elements and `formatAuthErrorWithRepo`
2. `refactor:` split `registerTools` into per-family files — **moves only**
3. `refactor:` `getRunTyped` dispatch map; single source of truth for the element set
4. `refactor:` extract the session quartet with a documented contract
5. `docs:` `doc.go`, exported doc comments

**Agent C — packages `cmd`, `config`, `tests` + repo root** (findings 29–38, 42–43, 45)
1. `test:` characterization tests for `runLogs` argument resolution and `config.Load` precedence
2. `refactor:` split `cmd/root.go` and `config/config.go` — **moves only**
3. `refactor:` kill `loadConfigWithOptions`'s bool; extract `registerRootFlags`
4. `fix:` unchecked `Close()` in tests (test-only, enables `errcheck`)
5. `ci:` `.golangci.yml`, `goleak` in `TestMain` for all packages
6. `docs:` README drift sweep — **execute every command**; `docs/architecture.md`; ADR 0003

Then, sequentially after all three: `docs/refactor-report.md`, and only if requested,
`fix:` commits for the two confirmed bugs.

### `BREAKING:`

None planned. Every exported identifier keeps its name and signature. New option structs
(finding 6) are additive; the existing long-parameter functions stay as wrappers.

### New dependencies

- `go.uber.org/goleak` (test-only, finding 45) — replaces nothing; stdlib has no goroutine-leak
  detector, and §7.6 requires one. Test-only import, not in the shipped binary.
