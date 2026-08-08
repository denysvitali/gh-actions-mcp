package github

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTimingStatsFromSamples(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		durations []float64
		want      TimingStats
	}{
		{
			name:      "no samples yields a zero value",
			durations: nil,
			want:      TimingStats{},
		},
		{
			name:      "a single sample is its own min, max, mean and median",
			durations: []float64{42},
			want:      TimingStats{Count: 1, AverageSeconds: 42, MedianSeconds: 42, MinSeconds: 42, MaxSeconds: 42},
		},
		{
			name:      "odd count takes the middle element",
			durations: []float64{30, 10, 20},
			want:      TimingStats{Count: 3, AverageSeconds: 20, MedianSeconds: 20, MinSeconds: 10, MaxSeconds: 30},
		},
		{
			name:      "even count averages the two middle elements",
			durations: []float64{40, 10, 30, 20},
			want:      TimingStats{Count: 4, AverageSeconds: 25, MedianSeconds: 25, MinSeconds: 10, MaxSeconds: 40},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			samples := make([]*TimingSample, 0, len(tt.durations))
			for _, d := range tt.durations {
				samples = append(samples, &TimingSample{DurationSeconds: d})
			}
			got := timingStatsFromSamples(samples)
			assert.Equal(t, tt.want.Count, got.Count)
			assert.InDelta(t, tt.want.AverageSeconds, got.AverageSeconds, 0.001)
			assert.InDelta(t, tt.want.MedianSeconds, got.MedianSeconds, 0.001)
			assert.InDelta(t, tt.want.MinSeconds, got.MinSeconds, 0.001)
			assert.InDelta(t, tt.want.MaxSeconds, got.MaxSeconds, 0.001)
		})
	}
}

func TestDeltaPercent(t *testing.T) {
	t.Parallel()

	assert.Equal(t, 0.0, deltaPercent(5, 0), "a zero baseline yields zero, never a division by zero")
	assert.InDelta(t, 50.0, deltaPercent(5, 10), 0.001)
	assert.InDelta(t, -50.0, deltaPercent(-5, 10), 0.001)
}

func TestCompareTimingSample(t *testing.T) {
	t.Parallel()

	sample := &TimingSample{RunID: 3, DurationSeconds: 120}
	baseline := &TimingStats{AverageSeconds: 100}

	t.Run("without a previous sample the previous deltas stay zero", func(t *testing.T) {
		t.Parallel()

		got := compareTimingSample(sample, baseline, nil)
		assert.InDelta(t, 20.0, got.DeltaFromAverageSeconds, 0.001)
		assert.InDelta(t, 20.0, got.DeltaFromAveragePercent, 0.001)
		assert.Zero(t, got.DeltaFromPreviousSeconds)
		assert.Zero(t, got.DeltaFromPreviousPercent)
	})

	t.Run("with a previous sample both deltas are filled in", func(t *testing.T) {
		t.Parallel()

		got := compareTimingSample(sample, baseline, &TimingSample{DurationSeconds: 80})
		assert.InDelta(t, 40.0, got.DeltaFromPreviousSeconds, 0.001)
		assert.InDelta(t, 50.0, got.DeltaFromPreviousPercent, 0.001)
	})
}

func TestIndexOfTimingSample(t *testing.T) {
	t.Parallel()

	samples := []*TimingSample{{RunID: 1}, {RunID: 2}}
	assert.Equal(t, 0, indexOfTimingSample(samples, 1))
	assert.Equal(t, 1, indexOfTimingSample(samples, 2))
	assert.Equal(t, -1, indexOfTimingSample(samples, 99))
	assert.Equal(t, -1, indexOfTimingSample(nil, 1))
}

func TestSamplesExcludingIndex(t *testing.T) {
	t.Parallel()

	samples := []*TimingSample{{RunID: 1}, {RunID: 2}, {RunID: 3}}

	got := samplesExcludingIndex(samples, 1)
	require.Len(t, got, 2)
	assert.Equal(t, int64(1), got[0].RunID)
	assert.Equal(t, int64(3), got[1].RunID)

	// An out-of-range index returns the input untouched rather than panicking.
	assert.Equal(t, samples, samplesExcludingIndex(samples, -1))
	assert.Equal(t, samples, samplesExcludingIndex(samples, 3))
}

func TestPreviousTimingSample(t *testing.T) {
	t.Parallel()

	samples := []*TimingSample{{RunID: 1}, {RunID: 2}}

	// Samples are newest-first, so "previous" is the next index.
	require.NotNil(t, previousTimingSample(samples, 0))
	assert.Equal(t, int64(2), previousTimingSample(samples, 0).RunID)
	assert.Nil(t, previousTimingSample(samples, 1), "the oldest sample has no predecessor")
	assert.Nil(t, previousTimingSample(samples, -1))
}

func TestBuildJobBreakdown(t *testing.T) {
	t.Parallel()

	jobsByRun := map[int64][]*Job{
		3: {
			{Name: "build", DurationSeconds: 300},
			{Name: "lint", DurationSeconds: 60},
			{Name: "skipped", DurationSeconds: 0}, // zero-duration jobs are dropped
		},
		2: {{Name: "build", DurationSeconds: 200}, {Name: "lint", DurationSeconds: 70}},
		1: {{Name: "BUILD", DurationSeconds: 200}}, // name matching is case-insensitive
	}

	items := buildJobBreakdown(jobsByRun, 3)
	require.Len(t, items, 2, "only focus-run jobs with a positive duration appear")

	assert.Equal(t, "build", items[0].JobName, "the largest positive delta sorts first")
	assert.InDelta(t, 300.0, items[0].DurationSeconds, 0.001)
	assert.InDelta(t, 200.0, items[0].AverageDurationSeconds, 0.001)
	assert.InDelta(t, 100.0, items[0].DeltaFromAverageSeconds, 0.001)
	assert.InDelta(t, 50.0, items[0].DeltaFromAveragePercent, 0.001)

	assert.Equal(t, "lint", items[1].JobName)
	assert.InDelta(t, -10.0, items[1].DeltaFromAverageSeconds, 0.001)
}

func TestBuildJobBreakdown_NoBaseline(t *testing.T) {
	t.Parallel()

	// With only the focus run present there is no baseline, so the average is 0
	// and the percentage delta collapses to 0 rather than dividing by zero.
	items := buildJobBreakdown(map[int64][]*Job{1: {{Name: "build", DurationSeconds: 100}}}, 1)
	require.Len(t, items, 1)
	assert.Zero(t, items[0].AverageDurationSeconds)
	assert.InDelta(t, 100.0, items[0].DeltaFromAverageSeconds, 0.001)
	assert.Zero(t, items[0].DeltaFromAveragePercent)
}

func TestBuildStepBreakdown(t *testing.T) {
	t.Parallel()

	jobsByRun := map[int64][]*Job{
		2: {
			{Name: "build", Steps: []*Step{
				{Name: "Compile", DurationSeconds: 200},
				{Name: "Upload", DurationSeconds: 10},
				{Name: "Skipped", DurationSeconds: 0},
			}},
			{Name: "lint", Steps: []*Step{{Name: "Vet", DurationSeconds: 50}}},
		},
		1: {
			{Name: "build", Steps: []*Step{
				{Name: "Compile", DurationSeconds: 100},
				{Name: "Upload", DurationSeconds: 20},
			}},
			{Name: "lint", Steps: []*Step{{Name: "Vet", DurationSeconds: 40}}},
		},
	}

	t.Run("no job filter covers every job in the focus run", func(t *testing.T) {
		t.Parallel()

		items := buildStepBreakdown(jobsByRun, 2, "")
		require.Len(t, items, 3)
		assert.Equal(t, "Compile", items[0].StepName)
		assert.Equal(t, "build", items[0].JobName)
		assert.InDelta(t, 100.0, items[0].DeltaFromAverageSeconds, 0.001)
	})

	t.Run("a job filter restricts both the focus and the baseline", func(t *testing.T) {
		t.Parallel()

		items := buildStepBreakdown(jobsByRun, 2, "LINT")
		require.Len(t, items, 1, "the filter is case-insensitive")
		assert.Equal(t, "Vet", items[0].StepName)
		assert.InDelta(t, 40.0, items[0].AverageDurationSeconds, 0.001)
	})

	t.Run("an unknown job filter yields no items", func(t *testing.T) {
		t.Parallel()

		assert.Empty(t, buildStepBreakdown(jobsByRun, 2, "nope"))
	})
}

func TestSortAndLimitTimingBreakdown(t *testing.T) {
	t.Parallel()

	items := []*TimingBreakdownItem{
		{JobName: "a", DeltaFromAverageSeconds: 10, DurationSeconds: 100},
		{JobName: "b", DeltaFromAverageSeconds: 10, DurationSeconds: 200},
		{JobName: "c", DeltaFromAverageSeconds: 20, DurationSeconds: 5},
	}
	sortTimingBreakdown(items)

	assert.Equal(t, "c", items[0].JobName, "the largest delta wins")
	assert.Equal(t, "b", items[1].JobName, "equal deltas break the tie on duration")
	assert.Equal(t, "a", items[2].JobName)

	assert.Len(t, limitTimingBreakdown(items, 2), 2)
	assert.Len(t, limitTimingBreakdown(items, 10), 3)
}

func TestLimitTimingRuns(t *testing.T) {
	t.Parallel()

	runs := []*WorkflowRun{
		{ID: 5, RunNumber: 5},
		{ID: 4, RunNumber: 4},
		{ID: 3, RunNumber: 3},
		{ID: 2, RunNumber: 2},
		{ID: 1, RunNumber: 1},
	}

	t.Run("a non-positive limit or a short slice is a no-op", func(t *testing.T) {
		t.Parallel()

		assert.Equal(t, runs, limitTimingRuns(runs, 0, 0))
		assert.Equal(t, runs, limitTimingRuns(runs, 10, 0))
	})

	t.Run("without a focus run the newest runs are kept", func(t *testing.T) {
		t.Parallel()

		got := limitTimingRuns(runs, 2, 0)
		require.Len(t, got, 2)
		assert.Equal(t, int64(5), got[0].ID)
	})

	t.Run("a focus run inside the window is kept naturally", func(t *testing.T) {
		t.Parallel()

		got := limitTimingRuns(runs, 3, 4)
		require.Len(t, got, 3)
		assert.Equal(t, int64(4), got[1].ID)
	})

	t.Run("a focus run outside the window displaces the oldest kept run", func(t *testing.T) {
		t.Parallel()

		got := limitTimingRuns(runs, 3, 1)
		require.Len(t, got, 3)
		ids := []int64{got[0].ID, got[1].ID, got[2].ID}
		assert.Equal(t, []int64{5, 4, 1}, ids, "run 3 is dropped so the focus run fits, order stays newest-first")
	})

	t.Run("an unknown focus run falls back to the newest runs", func(t *testing.T) {
		t.Parallel()

		got := limitTimingRuns(runs, 2, 999)
		require.Len(t, got, 2)
		assert.Equal(t, int64(5), got[0].ID)
	})
}

func TestMatchesTimingRun(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		run        *WorkflowRun
		conclusion string
		want       bool
	}{
		{name: "nil run", run: nil, want: false},
		{name: "not completed", run: &WorkflowRun{Status: "in_progress", DurationSeconds: 10}, want: false},
		{name: "zero duration", run: &WorkflowRun{Status: "completed", DurationSeconds: 0}, want: false},
		{name: "completed with a duration", run: &WorkflowRun{Status: "completed", DurationSeconds: 10}, want: true},
		{
			name:       "conclusion filter matches",
			run:        &WorkflowRun{Status: "completed", DurationSeconds: 10, Conclusion: "success"},
			conclusion: "success",
			want:       true,
		},
		{
			name:       "conclusion filter does not match",
			run:        &WorkflowRun{Status: "completed", DurationSeconds: 10, Conclusion: "failure"},
			conclusion: "success",
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, matchesTimingRun(tt.run, tt.conclusion))
		})
	}
}

func TestAppendTimingRunIfMissing(t *testing.T) {
	t.Parallel()

	runs := []*WorkflowRun{{ID: 1}}
	assert.Len(t, appendTimingRunIfMissing(runs, &WorkflowRun{ID: 1}), 1, "an already-present run is not duplicated")
	assert.Len(t, appendTimingRunIfMissing(runs, &WorkflowRun{ID: 2}), 2)
}

func TestTimingSampleForScope(t *testing.T) {
	t.Parallel()

	run := &WorkflowRun{ID: 7, RunNumber: 7, DurationSeconds: 500, Conclusion: "success", CreatedAt: "2026-01-01"}
	jobs := []*Job{
		{Name: "build", DurationSeconds: 300, Steps: []*Step{
			{Name: "Compile", DurationSeconds: 200},
			{Name: "Zero", DurationSeconds: 0},
		}},
		{Name: "nodur", DurationSeconds: 0},
	}

	tests := []struct {
		name        string
		scope       string
		jobName     string
		stepName    string
		wantNil     bool
		wantSeconds float64
		wantErr     string
	}{
		{name: "workflow scope uses the run duration", scope: "workflow", wantSeconds: 500},
		{name: "job scope uses the job duration", scope: "job", jobName: "build", wantSeconds: 300},
		{name: "job scope is case-insensitive", scope: "job", jobName: "BUILD", wantSeconds: 300},
		{name: "unknown job yields no sample", scope: "job", jobName: "missing", wantNil: true},
		{name: "zero-duration job yields no sample", scope: "job", jobName: "nodur", wantNil: true},
		{name: "step scope uses the step duration", scope: "step", jobName: "build", stepName: "Compile", wantSeconds: 200},
		{name: "unknown step yields no sample", scope: "step", jobName: "build", stepName: "missing", wantNil: true},
		{name: "zero-duration step yields no sample", scope: "step", jobName: "build", stepName: "Zero", wantNil: true},
		{name: "unknown job in step scope yields no sample", scope: "step", jobName: "missing", stepName: "Compile", wantNil: true},
		{name: "unknown scope is an error", scope: "galaxy", wantErr: `unknown timing scope "galaxy"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			sample, err := timingSampleForScope(run, jobs, tt.scope, tt.jobName, tt.stepName)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			if tt.wantNil {
				assert.Nil(t, sample)
				return
			}
			require.NotNil(t, sample)
			assert.InDelta(t, tt.wantSeconds, sample.DurationSeconds, 0.001)
			assert.Equal(t, int64(7), sample.RunID)
			assert.Equal(t, "success", sample.Conclusion)
		})
	}
}

func TestNormalizeTimingName(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "unit tests", normalizeTimingName("  Unit Tests "))
	assert.Empty(t, normalizeTimingName("   "))
}
