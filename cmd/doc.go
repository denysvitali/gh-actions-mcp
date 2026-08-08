// Package cmd is the command-line surface of gh-actions-mcp: the cobra
// commands, their flags, and the transports the MCP server is served over.
//
// # What belongs here
//
// Argument parsing, flag registration, configuration assembly, and rendering
// results for a human. Nothing here knows how to call the GitHub API — that is
// package github — and nothing here defines an MCP tool — that is package mcp.
// If a piece of logic would be equally useful from the MCP server, it does not
// belong in this package.
//
// # File map
//
//	root.go         cobra wiring: rootCmd, infer-repo, Execute, getVersion
//	flags.go        every flag variable and its registration
//	config_load.go  config file + flags + git proxy detection -> *config.Config
//	logs.go         the `logs` command
//	tool.go         the `tool` command (invoke one MCP tool from the CLI)
//	transport.go    stdio and Streamable HTTP transports
//
// # Commands
//
//	gh-actions-mcp              serve MCP over --transport (stdio or http)
//	gh-actions-mcp infer-repo   print the owner/repo inferred from git origin
//	gh-actions-mcp logs         fetch and filter logs for a run or job
//	gh-actions-mcp tool <name>  invoke one MCP tool with --args '{...}'
//
// # Package-level variables
//
// Cobra binds flags to addresses, so every flag value is a package-level
// variable (see flags.go). They are written once, by flag parsing, before any
// command runs, and read-only afterwards. Tests that set them must not call
// t.Parallel; preserveCommandGlobals in the test files restores them.
//
// # Concurrency
//
// The only `go` statement in the whole repository is the HTTP listener in
// serveStreamableHTTP; its ownership contract is documented at the statement.
// Everything else in this package is single-goroutine.
package cmd
