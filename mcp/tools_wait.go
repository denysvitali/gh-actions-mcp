package mcp

// Tool declarations for the polling family. wait_for_run and wait_all take the
// identical argument contract and differ only in their completion predicate
// (whole run vs. every job), so their shared arguments are declared once below:
// a change to the timeout bounds of one is a change to both.

// waitRunID declares the run selector shared by wait_for_run and wait_all.
func (b toolBuilder) waitRunID() toolOption {
	return b.WithNumber("run_id",
		b.Description("The workflow run ID to wait for"),
		b.Required(),
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
		b.WithDescription("Wait silently for a workflow run to complete (no output during polling)"),
		b.ReadOnly(),
		b.repoOverrides(),
		b.waitRunID(),
		b.waitTimeoutMinutes(),
	), s.waitForRunTyped)

	// Tool: wait_all
	addTool(s, b.NewTool("wait_all",
		b.WithDescription("Wait silently for every job in a workflow run to complete, regardless of job status"),
		b.ReadOnly(),
		b.repoOverrides(),
		b.waitRunID(),
		b.waitTimeoutMinutes(),
	), s.waitAllTyped)

	// Tool: wait_for_commit_checks
	// This one waits on a ref rather than a run, so it shares only the timeout.
	addTool(s, b.NewTool("wait_for_commit_checks",
		b.WithDescription("Wait for all CI check runs for a commit ref (SHA, branch, or tag) to complete."),
		b.ReadOnly(),
		b.repoOverrides(),
		b.WithString("ref",
			b.Description("Git ref (commit SHA, branch name, or tag) - default: HEAD"),
		),
		b.waitTimeoutMinutes(),
	), s.waitForCommitChecksTyped)
}
