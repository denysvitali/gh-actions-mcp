package github

import (
	"sort"
)

func indexOfTimingSample(samples []*TimingSample, runID int64) int {
	for i, sample := range samples {
		if sample.RunID == runID {
			return i
		}
	}
	return -1
}

func samplesExcludingIndex(samples []*TimingSample, exclude int) []*TimingSample {
	if exclude < 0 || exclude >= len(samples) {
		return samples
	}
	result := make([]*TimingSample, 0, len(samples)-1)
	for i, sample := range samples {
		if i == exclude {
			continue
		}
		result = append(result, sample)
	}
	return result
}

func previousTimingSample(samples []*TimingSample, focusIndex int) *TimingSample {
	if focusIndex < 0 || focusIndex+1 >= len(samples) {
		return nil
	}
	return samples[focusIndex+1]
}

func timingStatsFromSamples(samples []*TimingSample) *TimingStats {
	if len(samples) == 0 {
		return &TimingStats{}
	}

	durations := make([]float64, 0, len(samples))
	for _, sample := range samples {
		durations = append(durations, sample.DurationSeconds)
	}
	sort.Float64s(durations)

	var total float64
	for _, duration := range durations {
		total += duration
	}

	median := durations[len(durations)/2]
	if len(durations)%2 == 0 {
		middle := len(durations) / 2
		median = (durations[middle-1] + durations[middle]) / 2
	}

	return &TimingStats{
		Count:          len(durations),
		AverageSeconds: total / float64(len(durations)),
		MedianSeconds:  median,
		MinSeconds:     durations[0],
		MaxSeconds:     durations[len(durations)-1],
	}
}

func compareTimingSample(sample *TimingSample, baseline *TimingStats, previous *TimingSample) *TimingComparison {
	comparison := &TimingComparison{
		TimingSample:            sample,
		DeltaFromAverageSeconds: sample.DurationSeconds - baseline.AverageSeconds,
		DeltaFromAveragePercent: deltaPercent(sample.DurationSeconds-baseline.AverageSeconds, baseline.AverageSeconds),
	}
	if previous != nil {
		comparison.DeltaFromPreviousSeconds = sample.DurationSeconds - previous.DurationSeconds
		comparison.DeltaFromPreviousPercent = deltaPercent(sample.DurationSeconds-previous.DurationSeconds, previous.DurationSeconds)
	}
	return comparison
}

func deltaPercent(delta, baseline float64) float64 {
	if baseline == 0 {
		return 0
	}
	return (delta / baseline) * 100
}

type stepBreakdownKey struct {
	JobName  string
	StepName string
}

func buildJobBreakdown(jobsByRun map[int64][]*Job, focusRunID int64) ([]*TimingBreakdownItem, int) {
	focusJobs := jobsByRun[focusRunID]
	baseline := make(map[string][]float64)
	for runID, jobs := range jobsByRun {
		if runID == focusRunID {
			continue
		}
		for _, job := range jobs {
			if job.DurationSeconds <= 0 {
				continue
			}
			key := normalizeTimingName(job.Name)
			baseline[key] = append(baseline[key], job.DurationSeconds)
		}
	}

	items := make([]*TimingBreakdownItem, 0, len(focusJobs))
	for _, job := range focusJobs {
		if job.DurationSeconds <= 0 {
			continue
		}
		stats := timingStatsFromDurations(baseline[normalizeTimingName(job.Name)])
		delta := job.DurationSeconds - stats.AverageSeconds
		items = append(items, &TimingBreakdownItem{
			JobName:                 job.Name,
			DurationSeconds:         job.DurationSeconds,
			AverageDurationSeconds:  stats.AverageSeconds,
			DeltaFromAverageSeconds: delta,
			DeltaFromAveragePercent: deltaPercent(delta, stats.AverageSeconds),
		})
	}
	sortTimingBreakdown(items)
	return limitTimingBreakdown(items, 10)
}

func buildStepBreakdown(jobsByRun map[int64][]*Job, focusRunID int64, jobName string) ([]*TimingBreakdownItem, int) { //nolint:gocognit // Baseline and focus traversal intentionally mirror each other.
	focusJobs := jobsByRun[focusRunID]
	baseline := make(map[stepBreakdownKey][]float64)
	normalizedJobName := normalizeTimingName(jobName)
	for runID, jobs := range jobsByRun {
		if runID == focusRunID {
			continue
		}
		for _, job := range jobs {
			if normalizedJobName != "" && normalizeTimingName(job.Name) != normalizedJobName {
				continue
			}
			for _, step := range job.Steps {
				if step.DurationSeconds <= 0 {
					continue
				}
				key := stepBreakdownKey{
					JobName:  normalizeTimingName(job.Name),
					StepName: normalizeTimingName(step.Name),
				}
				baseline[key] = append(baseline[key], step.DurationSeconds)
			}
		}
	}

	items := make([]*TimingBreakdownItem, 0)
	for _, job := range focusJobs {
		if normalizedJobName != "" && normalizeTimingName(job.Name) != normalizedJobName {
			continue
		}
		for _, step := range job.Steps {
			if step.DurationSeconds <= 0 {
				continue
			}
			key := stepBreakdownKey{
				JobName:  normalizeTimingName(job.Name),
				StepName: normalizeTimingName(step.Name),
			}
			stats := timingStatsFromDurations(baseline[key])
			delta := step.DurationSeconds - stats.AverageSeconds
			items = append(items, &TimingBreakdownItem{
				JobName:                 job.Name,
				StepName:                step.Name,
				DurationSeconds:         step.DurationSeconds,
				AverageDurationSeconds:  stats.AverageSeconds,
				DeltaFromAverageSeconds: delta,
				DeltaFromAveragePercent: deltaPercent(delta, stats.AverageSeconds),
			})
		}
	}
	sortTimingBreakdown(items)
	return limitTimingBreakdown(items, 10)
}

func timingStatsFromDurations(durations []float64) *TimingStats {
	samples := make([]*TimingSample, 0, len(durations))
	for _, duration := range durations {
		samples = append(samples, &TimingSample{DurationSeconds: duration})
	}
	return timingStatsFromSamples(samples)
}

func sortTimingBreakdown(items []*TimingBreakdownItem) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].DeltaFromAverageSeconds == items[j].DeltaFromAverageSeconds {
			return items[i].DurationSeconds > items[j].DurationSeconds
		}
		return items[i].DeltaFromAverageSeconds > items[j].DeltaFromAverageSeconds
	})
}

func limitTimingBreakdown(items []*TimingBreakdownItem, limit int) ([]*TimingBreakdownItem, int) {
	if len(items) <= limit {
		return items, 0
	}
	return items[:limit], len(items) - limit
}
