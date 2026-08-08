// Package config resolves the settings gh-actions-mcp runs with.
//
// # What belongs here
//
// Everything that answers "where does this value come from?" — layering
// defaults, a YAML config file, and environment variables into one [Config],
// plus the token-discovery fallbacks (macOS keychain, gh CLI) and the
// validation that a usable token and repository are present. Nothing in this
// package talks to the GitHub API.
//
// # Precedence
//
// For every key, later layers win:
//
//  1. Built-in defaults (see setDefaults).
//  2. The config file — either the explicit --config path, or the first of
//     $HOME/.config/gh-actions-mcp/config.yaml and
//     /etc/gh-actions-mcp/config.yaml. The working directory is deliberately
//     not searched; see readConfigFile.
//  3. Environment variables, GITHUB_* taking precedence over GH_*.
//
// The CLI flags sit above all three, but they are applied by package cmd, not
// here, so that this package stays independent of cobra.
//
// Token discovery is separate from loading and happens in
// [Config.ValidateToken], because it can be slow (it shells out) and must run
// after any proxy credentials have been resolved.
//
// # Concurrency
//
// [Config] is a plain value with no internal synchronisation: it is populated
// during startup, before any goroutine that reads it exists, and is treated as
// read-only afterwards. The package-level logger set by [SetLogger] follows the
// same rule — call it once, during startup.
//
// # Build tags
//
// Keychain access is split across three files by build tag, and exactly one is
// compiled:
//
//	keychain_darwin.go        darwin && cgo   — the real go-keychain query
//	keychain_darwin_nocgo.go  darwin && !cgo  — fails, keychain needs cgo
//	keychain.go               !darwin         — fails, keychain is macOS only
//
// All three define getTokenFromKeychain, so [Config.ValidateToken] never needs
// a runtime platform check to compile.
package config
