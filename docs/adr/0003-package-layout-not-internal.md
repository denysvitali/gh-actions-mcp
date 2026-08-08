# ADR 0003 — Packages stay at the repository root, not under `internal/`

- **Status:** Accepted
- **Date:** 2026-07-24

## Context

The 2026-07 refactor (see `docs/refactor-audit.md`) found four packages at the
repository root: `cmd`, `config`, `github`, `mcp`, plus `main` and a
build-tagged `tests`. A common Go convention is to move packages that are not
meant to be imported from outside the module under `internal/`, which makes the
compiler enforce that boundary.

Two things argued against doing that here:

1. The module is published as `github.com/denysvitali/gh-actions-mcp`. Moving a
   package to `internal/` changes its import path and makes it *unimportable*
   from outside the module. For anyone who already imports `…/github` or
   `…/config` — and we cannot know whether anyone does — that is a breaking
   change with no deprecation path.
2. It buys the reader nothing. The four packages are already domain-shaped:
   `github` is the API client, `mcp` is the protocol layer, `config` is settings
   resolution, `cmd` is the CLI. A reader looking for the log-filtering code is
   helped by knowing it is in `github`, not by an extra `internal/` segment in
   front of it. `internal/` communicates "do not import", which is a
   distribution concern, not a comprehension one.

The actual complaint in the audit was different: `github/client.go` was 3,470
lines and `mcp/server.go` was 722. The problem was never *where* the packages
lived; it was that a newcomer could not guess which **file** anything was in.

## Decision

Package import paths stay exactly as they are. No package moves under
`internal/`, and no package is renamed.

The dependency direction is documented and enforced by review instead:
`cmd → mcp → github, config`, one-way and acyclic (see
`docs/architecture.md`). Neither `github` nor `config` imports `mcp` or `cmd`.

The refactor spends its effort on **intra-package file splitting** instead:
one file per concern, no file over 500 lines, and a `doc.go` per package
stating what belongs in it.

## Consequences

**Good**

- Zero breaking changes: every exported identifier keeps its import path, name
  and signature. Any external importer keeps compiling.
- The refactor's whole diff is moves and splits, which is reviewable and cheap
  to revert per file.
- `doc.go` plus a file-per-concern layout answers "where does this live?"
  directly, which is what the audit was actually about.

**Bad / accepted costs**

- Nothing stops an external module from importing `…/github` and depending on
  internals we consider private. We accept that: we already shipped these paths,
  so the guarantee is already implicitly given.
- The `cmd → mcp → github` direction is a convention, not a compiler rule. A
  future import cycle or a `github`-imports-`mcp` mistake would be caught by
  review and by the Go compiler's cycle check, not by package visibility. If it
  ever becomes a real problem, an import-boundary linter (`depguard` is already
  enabled in `.golangci.yml`) can encode the rule without moving any files.
- `tests` remains a layer name rather than a domain name. It holds only
  build-tagged integration and live-proxy tests, so the cost is contained;
  recorded as an accepted deviation (audit finding 43).
