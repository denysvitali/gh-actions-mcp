// Package mcp is the Model Context Protocol transport layer over package
// github: it declares the tool and resource surface an MCP client sees, decodes
// and validates arguments, and renders results into MCP content.
//
// # What belongs here
//
//   - Tool and resource declarations — names, descriptions, argument schemas,
//     defaults, enums and annotations (tools_*.go, resources.go).
//   - Argument and result wire types (tool_inputs.go).
//   - Handlers that translate a decoded request into one or more package github
//     calls and render the answer (handlers_*.go, run_elements.go).
//   - Presentation: result envelopes, log truncation, error-message
//     classification, opaque pagination cursors (results.go, errors_format.go,
//     pagination.go).
//
// # What does not belong here
//
// GitHub semantics. Anything that knows how the Actions API paginates, what a
// job's steps look like, how to unpack an artifact, or when a run counts as
// finished belongs in package github. A handler in this package should read as a
// short sequence of argument resolution, one client call, and one result render;
// when a handler starts growing GitHub knowledge, that knowledge is in the wrong
// package.
//
// The dependency direction is one-way and must stay that way:
//
//	cmd → mcp → github
//
// Package github never imports this package, and this package never imports
// cmd. That is what keeps the tool surface swappable and the GitHub client
// usable on its own.
//
// # Wire contract
//
// The MCP surface is a published contract. Tool names, descriptions, argument
// names, defaults, enum values and their order, result shapes and user-facing
// error text are all observable by clients, and changing any of them is a
// breaking change even though no Go identifier moved. mcp/tools_schema_test.go
// pins the whole registered surface against mcp/testdata/tools.golden.json, and
// results_test.go and errors_format_test.go pin the rendered output. Regenerate
// the golden file deliberately, never to make a test pass:
//
//	go test ./mcp/ -run Golden -update
//
// Two derivations exist to stop that contract from drifting internally: the
// get_run "element" enum is generated from runElements (run_elements.go), and the
// per-call owner/repo override pair is declared once as toolBuilder.repoOverrides.
//
// # Concurrency model
//
// An MCPServer is safe for concurrent use. It owns no mutable service state
// after construction: the GitHub client, config, logger and version are
// write-once, and per-call repository overrides get their own client rather than
// mutating the shared one.
//
// The one exception is the loopback session behind InvokeTool, which is created
// lazily and guarded by a mutex; localSession in invoke.go documents exactly
// which fields that mutex guards and why a mutex is the right primitive there.
// Everything else that looks like shared state is owned by the SDK: it manages
// its own sessions, one per connected client, and calls handlers concurrently.
// Handlers must therefore stay free of package-level mutable state.
package mcp
