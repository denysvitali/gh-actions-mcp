# Bugs found during refactoring — reported, not fixed

Per the refactoring rules, behaviour changes are never bundled with refactoring. Each bug
below is reproduced against commit `311ac60` and left in place. Fix them in separate
commits prefixed `fix:`.

---

## 1. `IsHTTPError` does not see a wrapped `*HTTPError`

**Location:** `github/client.go:45`

`IsHTTPError` uses a bare type assertion instead of `errors.As`, so an `*HTTPError` wrapped
with `%w` is not recognised. It then falls back to regex-matching the error *message* for
`status code: (\d+)` — and `HTTPError.Error()` formats as `"<msg>: HTTP 404"`, which that
regex does not match. Result: the wrapped case returns `false`.

**Reproduction** (verified 2026-07-24):

```go
base := &github.HTTPError{StatusCode: 404, Message: "nope"}
wrapped := fmt.Errorf("get run: %w", base)

github.IsHTTPError(base, 404)    // true
github.IsHTTPError(wrapped, 404) // false   <-- wrong
errors.As(wrapped, new(*github.HTTPError)) // true
```

**Impact:** every caller that wraps a GitHub error before checking for 404 silently takes the
"not a 404" path. Callers that pass the error through unwrapped are unaffected, which is why
this has not shown up yet.

**Fix:** `var httpErr *HTTPError; if errors.As(err, &httpErr) { return httpErr.StatusCode == statusCode }`.

Related, same function: the fallback `regexp.MustCompile` is recompiled on **every call**
(`github/client.go:55`). That is a performance defect, not a correctness one — tracked as
finding 8 in `docs/refactor-audit.md` and safe to fix as a refactor.

---

## 2. `InferRepoFromOrigin` accepts non-GitHub hosts as the owner

**Location:** `github/client.go:1280-1289`

The HTTPS branch strips the scheme, then strips the literal prefix `github.com/`. When the
host is *not* `github.com`, the host survives into the path and becomes the owner whenever
the remaining path splits into exactly two parts.

**Reproduction** (verified 2026-07-24):

```go
github.InferRepoFromOrigin("https://example.com/repo.git")
// returns owner="example.com", repo="repo", err=nil   <-- wrong

github.ParseGitURL("https://example.com/repo.git")
// returns err="not a GitHub URL: https://example.com/repo.git"   <-- correct
```

Three-segment non-GitHub URLs (`https://gitlab.com/o/r.git`) happen to error out, so the bug
only bites on two-segment paths.

**Impact:** a repository whose `origin` points at a non-GitHub host can be silently inferred
as `example.com/repo` and then queried against api.github.com. The user sees a confusing 404
rather than "not a GitHub remote".

**Why it exists:** `InferRepoFromOrigin` is a fork of `ParseGitURL` (`github/repo_detector.go:50`).
The bare and SSH branches are identical; the HTTPS branch diverged and lost the `isGitHubURL`
host check, the `git://` support, and the `containsToken` guard. This is finding 9 in
`docs/refactor-audit.md`.

**Fix:** route `InferRepoFromOrigin` through `ParseGitURL`. Note this *is* a behaviour change
(it starts rejecting non-GitHub hosts and token-bearing URLs), which is exactly why it is here
and not in a refactor commit.
