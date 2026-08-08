package mcp

// Tool declarations for the read-only analysis family: timing statistics, check
// status rollups and failure diagnosis. All three derive their answers from
// workflow runs, so none of them needs Checks API permissions.

// registerTimingTools declares analyze_timing.
func (s *MCPServer) registerTimingTools(b toolBuilder) {
	// Tool: analyze_timing
	addTool(s, b.NewTool("analyze_timing",
		b.WithDescription("Analyze workflow, job, or step durations across recent runs to compare a specific CI run against recent history and surface slow spots."),
		b.ReadOnly(),
		b.repoOverrides(),
		b.WithString("workflow",
			b.Description("Workflow selector (name, path, or numeric ID). Required unless run_id is provided."),
		),
		b.WithNumber("run_id",
			b.Description("Optional: focus on a specific workflow run ID. When omitted, the latest matching run is used."),
		),
		b.WithString("branch",
			b.Description("Optional: branch to compare against. When omitted, compares against runs from all branches."),
		),
		b.WithString("job_name",
			b.Description("Optional: analyze a specific job across runs. Required when step_name is provided."),
		),
		b.WithString("step_name",
			b.Description("Optional: analyze a specific step within job_name across runs."),
		),
		b.WithString("conclusion",
			b.Description("Optional: only include runs with a specific conclusion (success, failure, cancelled, etc.)."),
		),
		b.WithNumber("limit",
			b.Description("Maximum number of recent runs to analyze (default: 10, max: 50)."),
			b.DefaultNumber(10),
			b.Minimum(1),
			b.Maximum(50),
		),
	), s.analyzeTimingTyped)
}

// registerCheckStatusTools declares get_check_status.
func (s *MCPServer) registerCheckStatusTools(b toolBuilder) {
	// Tool: get_check_status
	addTool(s, b.NewTool("get_check_status",
		b.WithDescription("Get workflow status summary for a commit/branch/tag (derived from workflow runs; no Checks API permission required)."),
		b.ReadOnly(),
		b.repoOverrides(),
		b.WithString("ref",
			b.Description("Git ref (commit SHA, branch name, or tag) - default: HEAD of current branch"),
		),
		b.WithString("check_name",
			b.Description("Optional: filter by specific check name"),
		),
		b.WithString("status",
			b.Description("Optional: filter by status (queued, in_progress, completed)"),
		),
		b.WithString("filter",
			b.Description("Return latest workflow statuses (default) or all statuses for the ref. Allowed: latest, all."),
			b.DefaultString("latest"),
			b.Enum("latest", "all"),
		),
		b.WithString("format",
			b.Description("Output format: summary (default), compact, or full"),
			b.DefaultString("summary"),
		),
	), s.getCheckStatusTyped)
}

// registerDiagnosisTools declares diagnose_failure.
func (s *MCPServer) registerDiagnosisTools(b toolBuilder) {
	// Tool: diagnose_failure
	addTool(s, b.NewTool("diagnose_failure",
		b.WithDescription("One-shot diagnosis of a failed workflow run: identifies failed jobs/steps, extracts error lines from logs, and optionally checks for flakiness. Returns a structured diagnosis with actionable error context."),
		b.ReadOnly(),
		b.repoOverrides(),
		b.WithNumber("run_id",
			b.Description("The workflow run ID to diagnose. If omitted, diagnoses the latest failed run on the current branch."),
		),
		b.WithBoolean("check_flakiness",
			b.Description("Compare against recent runs to detect flaky tests (default: true). Adds a few extra API calls."),
		),
		b.WithNumber("max_error_lines",
			b.Description("Maximum number of error lines to extract per job (default: 50)"),
			b.DefaultNumber(50),
		),
	), s.diagnoseFailureTyped)
}
