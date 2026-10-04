package mcp

// Tool declarations for the polling family. wait_for_run and wait_all take the
// identical argument contract and differ only in their completion predicate
// (fail-fast vs. terminal run and every job), so their shared arguments are declared once below:
// a change to the timeout bounds of one is a change to both.

// waitRunID declares the run selector shared by wait_for_run and wait_all.
func (b toolBuilder) waitRunID() toolOption {
	return b.WithNumber("run_id",
		b.Description("The workflow run ID to wait for"),
		b.Required(),
	)
}

// waitExpectedAttempt prevents a rerun wait from accepting the previous attempt.
func (b toolBuilder) waitExpectedAttempt() toolOption {
	return b.WithNumber("expected_attempt",
		b.Description("Expected run attempt, such as manage_run.expected_attempt after a rerun. A newer attempt is reported as superseded."),
		b.Minimum(1),
	)
}

// waitTimeoutMinutes declares the polling budget shared by every wait_* tool.
// The 120-minute ceiling is the transport's hard limit on a single request.
func (b toolBuilder) waitTimeoutMinutes() toolOption {
	return b.WithNumber("timeout_minutes",
		b.Description("Maximum time to wait in minutes (default: 30)"),
		b.DefaultNumber(defaultWaitTimeoutMinutes),
		b.Minimum(1),
		b.Maximum(120),
	)
}

// registerWaitTools declares wait_for_run, wait_all and wait_for_commit_checks.
func (s *MCPServer) registerWaitTools(b toolBuilder) {
	// Tool: wait_for_run
	addTool(s, b.NewTool("wait_for_run",
		b.WithDescription("Wait silently until the run completes or a job/step failure is observed. failure_observed does not mean the run has completed; use watch_run for bounded resumable monitoring."),
		b.ReadOnly(),
		b.repoOverrides(),
		b.waitRunID(),
		b.waitExpectedAttempt(),
		b.waitTimeoutMinutes(),
	), s.waitForRunTyped)

	// Tool: wait_all
	addTool(s, b.NewTool("wait_all",
		b.WithDescription("Wait silently for terminal run status and completion of every job across all pages, regardless of job conclusion."),
		b.ReadOnly(),
		b.repoOverrides(),
		b.waitRunID(),
		b.waitExpectedAttempt(),
		b.waitTimeoutMinutes(),
	), s.waitAllTyped)

	// Tool: wait_for_commit_checks
	// This one waits on a ref rather than a run, so it shares only the timeout.
	addTool(s, b.NewTool("wait_for_commit_checks",
		b.WithDescription("Resolve a ref to one immutable commit and wait for its discovered Actions workflows to complete. Does not cover third-party checks or prove all expected workflows registered; use verify_commit to name expected workflows."),
		b.ReadOnly(),
		b.repoOverrides(),
		b.WithString("ref",
			b.Description("Git ref (commit SHA, branch name, or tag) - default: HEAD"),
		),
		b.waitTimeoutMinutes(),
	), s.waitForCommitChecksTyped)
}
