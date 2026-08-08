package mcp

import (
	"encoding/json"
	"testing"

	"github.com/denysvitali/gh-actions-mcp/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sampleRun has a distinct value in every field, so a projection that reads the
// wrong source field produces a visibly wrong value rather than a coincidence.
func sampleRun() *github.WorkflowRun {
	return &github.WorkflowRun{
		ID:              111,
		Name:            "CI",
		Status:          "completed",
		Conclusion:      "success",
		Branch:          "main",
		HeadSHA:         "abc123",
		Event:           "push",
		Actor:           "octocat",
		CreatedAt:       "2026-01-01T00:00:00Z",
		UpdatedAt:       "2026-01-01T00:10:00Z",
		StartedAt:       "2026-01-01T00:01:00Z",
		URL:             "https://github.com/o/r/actions/runs/111",
		RunNumber:       42,
		WorkflowID:      777,
		DurationSeconds: 540,
	}
}

// TestFormatRunsProjections pins the JSON produced for every list_runs format.
// These field mappings are the wire contract of list_runs and of get_run
// element=info, which share them.
func TestFormatRunsProjections(t *testing.T) {
	t.Parallel()

	tests := []struct {
		format string
		want   string
	}{
		{
			format: "minimal",
			want:   `[{"id":111,"name":"CI","status":"completed","conclusion":"success","created_at":"2026-01-01T00:00:00Z","duration":540}]`,
		},
		{
			format: "compact",
			want: `[{"id":111,"name":"CI","status":"completed","conclusion":"success","created_at":"2026-01-01T00:00:00Z","duration":540,` +
				`"branch":"main","sha":"abc123","event":"push","actor":"octocat","url":"https://github.com/o/r/actions/runs/111"}]`,
		},
		{
			// The empty format falls back to compact, which is the schema default.
			format: "",
			want: `[{"id":111,"name":"CI","status":"completed","conclusion":"success","created_at":"2026-01-01T00:00:00Z","duration":540,` +
				`"branch":"main","sha":"abc123","event":"push","actor":"octocat","url":"https://github.com/o/r/actions/runs/111"}]`,
		},
		{
			// completed_at is deliberately updated_at: the API has no separate
			// completion timestamp on a run.
			format: "full",
			want: `[{"id":111,"name":"CI","status":"completed","conclusion":"success","branch":"main","event":"push","actor":"octocat",` +
				`"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:10:00Z","url":"https://github.com/o/r/actions/runs/111",` +
				`"run_number":42,"workflow_id":777,"head_sha":"abc123","started_at":"2026-01-01T00:01:00Z",` +
				`"completed_at":"2026-01-01T00:10:00Z","duration":540}]`,
		},
	}

	for _, tc := range tests {
		t.Run("format="+tc.format, func(t *testing.T) {
			t.Parallel()
			encoded, err := json.Marshal(formatRuns([]*github.WorkflowRun{sampleRun()}, tc.format))
			require.NoError(t, err)
			assert.JSONEq(t, tc.want, string(encoded))
			assert.Equal(t, tc.want, string(encoded), "field order is part of the rendered output")
		})
	}
}

// TestGetRunInfoProjectionsMatchListRuns pins that get_run element=info renders a
// run exactly the way list_runs does. The two share one projection; if they ever
// diverge, one of the two tools is silently reporting a different shape.
func TestGetRunInfoProjectionsMatchListRuns(t *testing.T) {
	t.Parallel()

	run := sampleRun()

	compactFromList, err := json.Marshal(formatRuns([]*github.WorkflowRun{run}, "compact")[0])
	require.NoError(t, err)
	compactDirect, err := json.Marshal(compactRun(run))
	require.NoError(t, err)
	assert.Equal(t, string(compactFromList), string(compactDirect))

	fullFromList, err := json.Marshal(formatRuns([]*github.WorkflowRun{run}, "full")[0])
	require.NoError(t, err)
	fullDirect, err := json.Marshal(fullRun(run))
	require.NoError(t, err)
	assert.Equal(t, string(fullFromList), string(fullDirect))
}
