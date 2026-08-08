package mcp

// Tool declarations for the workflow/run family. The schemas here are the MCP
// wire contract: descriptions, argument names, defaults and enum values are all
// pinned by tools_schema_test.go against testdata/tools.golden.json.

// registerRunTools declares the two listing tools: list_workflows and list_runs.
func (s *MCPServer) registerRunTools(b toolBuilder) {
	// Tool: list_workflows
	addTool(s, b.NewTool("list_workflows",
		b.WithDescription("List all workflows available in the repository"),
		b.ReadOnly(),
		b.repoOverrides(),
		b.WithNumber("limit",
			b.Description("Maximum number of workflows to return (default: 5)"),
			b.DefaultNumber(5),
			b.Minimum(1),
			b.Maximum(100),
		),
		b.WithString("format",
			b.Description("Output format: compact (default, single-line JSON), pretty (indented JSON), or full (detailed)"),
			b.DefaultString("compact"),
		),
		b.WithString("cursor",
			b.Description("Opaque cursor returned by the previous call"),
		),
	), s.listWorkflowsTyped)

	// Tool: list_runs
	addTool(s, b.NewTool("list_runs",
		b.WithDescription("List workflow runs with comprehensive filtering options"),
		b.ReadOnly(),
		b.repoOverrides(),
		b.WithAny("workflow_id",
			b.Description("Optional: workflow ID, workflow name, or workflow file path (e.g., '12345678', 'CI', or '.github/workflows/ci.yml') to filter by"),
		),
		b.WithString("branch",
			b.Description("Optional: Branch to filter by. When omitted, runs from all branches are included."),
		),
		b.WithString("status",
			b.Description("Optional: Status to filter by (queued, in_progress, completed, etc.)"),
		),
		b.WithString("conclusion",
			b.Description("Optional: Conclusion to filter by (success, failure, neutral, cancelled, etc.)"),
		),
		b.WithNumber("per_page",
			b.Description("Number of results per page (default: 5)"),
			b.DefaultNumber(5),
			b.Minimum(1),
			b.Maximum(100),
		),
		b.WithString("created_after",
			b.Description("Optional: ISO 8601 date string to filter runs created after this time"),
		),
		b.WithString("event",
			b.Description("Optional: Event to filter by (push, pull_request, etc.)"),
		),
		b.WithString("actor",
			b.Description("Optional: GitHub username to filter by"),
		),
		b.WithString("format",
			b.Description("Output format: minimal (basic fields), compact (default, most fields), or full (all fields)"),
			b.DefaultString("compact"),
		),
		b.WithString("cursor",
			b.Description("Opaque cursor returned by the previous call"),
		),
	), s.listRunsTyped)
}

// registerGetRunTool declares get_run, the multiplexed run inspection tool. Its
// "element" enum is derived from runElements so the schema, the validation error
// and the dispatch table cannot drift apart; the human-readable element list in
// the description is deliberately left literal, because it interleaves prose
// ("info (default)") with the names.
func (s *MCPServer) registerGetRunTool(b toolBuilder) {
	// Tool: get_run
	addTool(s, b.NewTool("get_run",
		b.WithDescription("Get workflow run details. Start with element=info, then use jobs/logs/log_sections/artifacts as needed."),
		b.ReadOnly(),
		b.repoOverrides(),
		b.WithNumber("run_id",
			b.Description("The workflow run ID"),
			b.Required(),
		),
		b.WithString("element",
			b.Description("Element to retrieve: info (default), jobs, logs, log_files, log_sections, artifacts, artifact_content. Invalid values return a validation error with allowed options."),
			b.DefaultString("info"),
			b.Enum(runElementNames...),
		),
		b.WithNumber("artifact_id",
			b.Description("For element=artifact_content: the artifact ID to get contents for"),
		),
		b.WithString("file_pattern",
			b.Description("For element=logs or artifact_content: glob pattern to filter files (e.g., '*.log', 'build/*')"),
		),
		b.WithNumber("max_file_size",
			b.Description("For element=artifact_content: maximum size of individual files to read in bytes (default: 1MB)"),
			b.DefaultNumber(defaultMaxArtifactFileSize),
		),
		b.WithNumber("job_id",
			b.Description("For element=logs or element=log_sections: specific job ID to get logs/sections for"),
		),
		b.WithNumber("attempt_number",
			b.Description("For element=jobs: attempt number for the jobs (default: latest)"),
		),
		b.getRunLogArguments(),
		b.WithString("format",
			b.Description("For element=info, jobs, artifacts, log_files: output format (compact/full, default: compact)"),
			b.DefaultString("compact"),
		),
	), s.getRunTyped)
}

// getRunLogArguments declares the get_run arguments that only apply to
// element=logs: the line window, the two search filters and the section
// selector. They are grouped because they are one feature — narrowing a log
// stream — inside a schema that is otherwise a flat union of every element's
// arguments. See runElementLogs for how they are consumed.
func (b toolBuilder) getRunLogArguments() toolOption {
	return combine(
		b.WithBoolean("per_job",
			b.Description("For element=logs: get logs per-job instead of all logs combined"),
		),
		b.WithNumber("head",
			b.Description("For element=logs: return the first N lines of logs. Without head or tail, logs are auto-truncated to the last ~100 lines"),
		),
		b.WithNumber("tail",
			b.Description("For element=logs: return the last N lines of logs (default: auto-truncated to last ~100 lines if neither head nor tail is specified)"),
		),
		b.WithNumber("offset",
			b.Description("For element=logs: skip first N lines before returning (0-based)"),
		),
		b.WithString("search",
			b.Description("For element=logs: search/filter logs to lines containing this substring (case-insensitive)"),
		),
		b.WithString("search_regex",
			b.Description("For element=logs: filter logs to lines matching this regex pattern"),
		),
		b.WithNumber("context",
			b.Description("For element=logs: number of lines to show before and after each search match (default: 0)"),
			b.DefaultNumber(0),
		),
		b.WithBoolean("no_headers",
			b.Description("For element=logs: don't print file headers (=== filename ===)"),
		),
		b.WithString("section",
			b.Description("For element=logs: extract a specific section by name/pattern (e.g., 'Build', 'Test'). GitHub Actions sections are marked with ##[group]Section Name"),
		),
	)
}

// registerManageRunTool declares manage_run, the only run-mutating tool.
func (s *MCPServer) registerManageRunTool(b toolBuilder) {
	// Tool: manage_run
	addTool(s, b.NewTool("manage_run",
		b.WithDescription("Manage a workflow run (cancel, rerun, or rerun failed jobs)"),
		b.Destructive(),
		b.repoOverrides(),
		b.WithNumber("run_id",
			b.Description("The workflow run ID to manage"),
			b.Required(),
		),
		b.WithString("action",
			b.Description("Action to perform: cancel, rerun, or rerun_failed"),
			b.Required(),
			b.Enum("cancel", "rerun", "rerun_failed"),
		),
	), s.manageRunTyped)
}
