package github

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/go-github/v89/github"
)

// FailureDiagnosis is the top-level result of diagnosing a failed workflow run
type FailureDiagnosis struct {
	RunAttempt int            `json:"run_attempt"`
	RunID      int64          `json:"run_id"`
	RunName    string         `json:"run_name"`
	RunURL     string         `json:"run_url"`
	Branch     string         `json:"branch"`
	HeadSHA    string         `json:"head_sha"`
	Conclusion string         `json:"conclusion"`
	FailedJobs []*FailedJob   `json:"failed_jobs"`
	Flakiness  *FlakinessInfo `json:"flakiness,omitempty"`
	Summary    string         `json:"summary"`
	Truncated  bool           `json:"truncated,omitempty"`
}

// FailedJob represents a job that failed within a workflow run
type FailedJob struct {
	JobID       int64                `json:"job_id"`
	JobName     string               `json:"job_name"`
	Conclusion  string               `json:"conclusion"`
	FailedSteps []*FailedStep        `json:"failed_steps"`
	ErrorLines  []string             `json:"error_lines"`
	ErrorSource string               `json:"error_source,omitempty"`
	Evidence    []DiagnosticEvidence `json:"evidence,omitempty"`
	Fingerprint string               `json:"fingerprint,omitempty"`
	Warnings    []string             `json:"warnings,omitempty"`
}

// DiagnosticEvidence records where an excerpt came from and how to expand it.
type DiagnosticEvidence struct {
	Source string `json:"source"`
	JobID  int64  `json:"job_id"`
	Line   int    `json:"line,omitempty"`
	Text   string `json:"text"`
}

// FailedStep represents a step that failed within a job
type FailedStep struct {
	Name       string `json:"name"`
	Number     int64  `json:"number"`
	Conclusion string `json:"conclusion"`
}

// FlakinessInfo contains information about whether this failure is likely a flake
type FlakinessInfo struct {
	RecentRuns             int               `json:"recent_runs_checked"`
	RecentFailures         int               `json:"recent_failures"`
	RecentSuccesses        int               `json:"recent_successes"`
	FingerprintComparisons int               `json:"fingerprint_comparisons"`
	MatchingFingerprints   int               `json:"matching_fingerprints"`
	SameFailureCount       int               `json:"same_failure_count"`
	Samples                []FlakinessSample `json:"samples,omitempty"`
	SameSHASuccesses       int               `json:"same_sha_successes"`
	SameSHAFailures        int               `json:"same_sha_failures"`
	Basis                  string            `json:"basis"`
	Verdict                string            `json:"verdict"` // "likely_flake", "likely_regression", "first_failure", "unknown"
}

// FlakinessSample makes the historical comparison auditable.
type FlakinessSample struct {
	RunID        int64                          `json:"run_id"`
	HeadSHA      string                         `json:"head_sha,omitempty"`
	Conclusion   string                         `json:"conclusion"`
	SameCommit   bool                           `json:"same_commit"`
	Fingerprints []FailureFingerprintComparison `json:"fingerprints,omitempty"`
}

// errorPatterns are regex patterns that identify error lines in CI logs.
// They are the fallback when a job log contains no GitHub Actions
// ##[error] annotations. The first pattern is deliberately narrower than
// "contains the word error": matching every echo "::error::..." line in a
// bash -x workflow dump is how diagnose_failure used to drown in script
// source (see gps-tracker HIL Test).
var errorPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)^.*error[:\[].*`),
	regexp.MustCompile(`(?i)^.*FAIL[:\s].*`),
	regexp.MustCompile(`(?i)^.*fatal[:\s].*`),
	regexp.MustCompile(`(?i)^.*panic[:\s].*`),
	regexp.MustCompile(`(?i)^.*exception[:\s].*`),
	regexp.MustCompile(`(?i)^.*traceback.*`),
	regexp.MustCompile(`(?i)^E\s+\w+`),        // pytest-style "E   AssertionError"
	regexp.MustCompile(`--- FAIL:`),           // Go test failures
	regexp.MustCompile(`(?i)exit code [1-9]`), // non-zero exit codes
	regexp.MustCompile(`(?i)command.*failed`),
	regexp.MustCompile(`(?i)process completed with exit code [1-9]`),
	regexp.MustCompile(`##\[error\]`), // GitHub Actions error annotations
}

// ghaErrorAnnotation matches a GitHub Actions error annotation after any
// timestamp prefix has been stripped. Used to prefer real runner-emitted
// errors over script-source lines that merely contain the word "error".
var ghaErrorAnnotation = regexp.MustCompile(`##\[error\]`)

// scriptSourceLine matches a bash -x / Actions debug dump of a script
// line. After ANSI is stripped on ingest these look like
// `echo "::error::foo"` or `echo "ERROR: foo"` — the workflow file, not
// the failure.
var scriptSourceLine = regexp.MustCompile(`(?i)^(?:\+\s*)?(?:echo|printf)\b`)

// DiagnoseFailure performs a comprehensive diagnosis of a failed workflow run.
// It fetches the run, identifies failed jobs and steps, extracts error lines from
// logs, and optionally checks for flakiness by comparing against recent runs.
func (c *Client) DiagnoseFailure(ctx context.Context, runID int64, checkFlakiness bool, maxLogLines int) (*FailureDiagnosis, error) { //nolint:funlen,gocognit,gocyclo // The orchestration reads as one diagnostic pipeline.
	if maxLogLines <= 0 {
		maxLogLines = 200
	}

	// 1. Get the run info
	run, err := c.GetWorkflowRun(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("failed to get run %d: %w", runID, err)
	}

	diagnosis := &FailureDiagnosis{
		RunID:      run.ID,
		RunAttempt: run.RunAttempt,
		RunName:    run.Name,
		RunURL:     run.URL,
		Branch:     run.Branch,
		HeadSHA:    run.HeadSHA,
		Conclusion: run.Conclusion,
	}

	if run.Conclusion == "success" {
		diagnosis.Summary = fmt.Sprintf("Run %d succeeded — nothing to diagnose", runID)
		return diagnosis, nil
	}

	// 2. Get jobs and identify failures
	jobs, err := c.GetWorkflowJobs(ctx, runID, "", run.RunAttempt)
	if err != nil {
		return nil, fmt.Errorf("failed to get jobs for run %d: %w", runID, err)
	}

	remainingLines := min(maxLogLines, 200)
	remainingBytes := maxDiagnosticBytes
	for _, job := range jobs {
		if job.Conclusion != "failure" && job.Conclusion != "cancelled" && job.Conclusion != "timed_out" {
			continue
		}

		if remainingLines <= 0 || remainingBytes <= 0 || len(diagnosis.FailedJobs) >= 20 {
			diagnosis.Truncated = true
			break
		}
		failedJob := &FailedJob{
			JobID:      job.ID,
			JobName:    job.Name,
			Conclusion: job.Conclusion,
		}

		// Identify failed steps
		for _, step := range job.Steps {
			if step.Conclusion == "failure" || step.Conclusion == "cancelled" || step.Conclusion == "timed_out" {
				failedJob.FailedSteps = append(failedJob.FailedSteps, &FailedStep{
					Name:       step.Name,
					Number:     step.Number,
					Conclusion: step.Conclusion,
				})
			}
		}

		annotations := c.getCheckRunAnnotationErrors(ctx, job.ID, remainingLines)
		logs, logErr := c.jobLogs(ctx, job.ID, LogViewOptions{NoHeaders: true})
		if logErr != nil {
			logs, logErr = c.jobLogsFromRunArchive(ctx, runID, job.ID, LogViewOptions{NoHeaders: true})
		}
		if logErr != nil {
			failedJob.Warnings = append(failedJob.Warnings, "Job logs unavailable; diagnosis may be incomplete")
		}
		failedJob.ErrorLines, failedJob.Evidence = mergeDiagnosticEvidence(job.ID, annotations, logs, remainingLines)
		switch {
		case len(annotations) > 0 && logs != "":
			failedJob.ErrorSource = "check_annotations_and_logs"
		case len(annotations) > 0:
			failedJob.ErrorSource = "check_annotations"
		default:
			failedJob.ErrorSource = "logs"
		}
		for i, line := range failedJob.ErrorLines {
			if len(line) > remainingBytes {
				failedJob.ErrorLines = failedJob.ErrorLines[:i]
				failedJob.Evidence = failedJob.Evidence[:i]
				break
			}
			remainingBytes -= len(line)
		}
		remainingLines -= len(failedJob.ErrorLines)
		failedJob.Fingerprint = diagnosticFingerprint(failedJob.ErrorLines)
		if logs != "" {
			failedJob.Fingerprint = diagnosticFingerprint(collectErrorLines(logs, 50))
		}

		diagnosis.FailedJobs = append(diagnosis.FailedJobs, failedJob)
	}

	// 4. Optional flakiness check
	if checkFlakiness && run.WorkflowID > 0 {
		diagnosis.Flakiness = c.checkFlakiness(ctx, run, diagnosis.FailedJobs)
	}

	// 5. Build summary
	diagnosis.Summary = c.buildDiagnosisSummary(diagnosis)
	if run.Status != "completed" {
		diagnosis.Summary = fmt.Sprintf("Run %d is still %s. %s", runID, run.Status, diagnosis.Summary)
	}

	return diagnosis, nil
}

// getCheckRunAnnotationErrors retrieves failure annotations for a workflow job.
// Workflow-job IDs are check-run IDs, so this endpoint avoids a log download when
// GitHub has already extracted an actionable error.
func (c *Client) getCheckRunAnnotationErrors(ctx context.Context, jobID int64, maxLines int) []string { //nolint:gocognit // Normalization and deduplication are intentionally co-located.
	if maxLines <= 0 {
		return nil
	}
	perPage := maxLines
	if perPage > 100 {
		perPage = 100
	}

	annotations, _, err := c.gh.Checks.ListCheckRunAnnotations(ctx, c.owner, c.repo, jobID, &github.ListOptions{PerPage: perPage})
	if err != nil {
		log.Debugf("Could not fetch check-run annotations for job %d: %v", jobID, err)
		return nil
	}

	result := make([]string, 0, len(annotations))
	seen := make(map[string]struct{})
	for _, annotation := range annotations {
		if annotation.GetAnnotationLevel() != "failure" {
			continue
		}
		message := strings.TrimSpace(annotation.GetMessage())
		if message == "" {
			continue
		}
		location := strings.TrimSpace(annotation.GetPath())
		if line := annotation.GetStartLine(); line > 0 {
			location = fmt.Sprintf("%s:%d", location, line)
		}
		if title := strings.TrimSpace(annotation.GetTitle()); title != "" {
			message = title + ": " + message
		}
		if location != "" {
			message = location + ": " + message
		}
		if _, ok := seen[message]; ok {
			continue
		}
		seen[message] = struct{}{}
		result = append(result, message)
		if len(result) == maxLines {
			break
		}
	}
	return result
}

// extractErrorLines fetches logs for a job and extracts lines matching error patterns.
// ##[error] annotations win over the regex fallback: they are what the runner
// actually emitted. Script-source lines (ANSI-colored bash -x dumps of
// `echo "::error::..."`) are skipped in both passes.
func (c *Client) extractErrorLines(ctx context.Context, runID, jobID int64, maxLines int) []string { //nolint:gocognit,nestif // Fallback and line normalization form one extraction path.
	logs, err := c.jobLogs(ctx, jobID, LogViewOptions{NoHeaders: true})
	if err != nil { //nolint:nestif // The archive fallback is intentionally bounded within the primary failure path.
		log.Debugf("Could not fetch logs for job %d: %v", jobID, err)
		if runID > 0 {
			archiveLogs, archiveErr := c.jobLogsFromRunArchive(ctx, runID, jobID, LogViewOptions{NoHeaders: true})
			if archiveErr == nil {
				logs = archiveLogs
			} else {
				return []string{fmt.Sprintf("[could not fetch logs: %v; archive fallback failed: %v]", err, archiveErr)}
			}
		} else {
			return []string{fmt.Sprintf("[could not fetch logs: %v]", err)}
		}
	}

	return collectErrorLines(logs, maxLines)
}

// collectErrorLines is the pure extractor behind extractErrorLines so the
// preference for ##[error] annotations and the skip of script-source lines
// can be unit-tested without standing up a GitHub client.
func collectErrorLines(logs string, maxLines int) []string {
	if maxLines <= 0 {
		maxLines = 200
	}
	lines, _ := mergeDiagnosticEvidence(0, nil, logs, maxLines)
	return lines
}

func normalizeErrorLine(line string) string {
	cleaned := strings.TrimSpace(stripANSI(line))
	if cleaned == "" {
		return ""
	}
	if len(cleaned) > 30 && cleaned[4] == '-' && cleaned[10] == 'T' {
		if spaceIdx := strings.Index(cleaned, " "); spaceIdx > 0 && spaceIdx < 35 {
			cleaned = strings.TrimSpace(cleaned[spaceIdx+1:])
		}
	}
	if scriptSourceLine.MatchString(cleaned) {
		return ""
	}
	return cleaned
}

func isErrorLine(line string, annotationsOnly bool) bool {
	if annotationsOnly {
		return ghaErrorAnnotation.MatchString(line)
	}
	if scriptSourceLine.MatchString(line) {
		return false
	}
	for _, pattern := range errorPatterns {
		if pattern.MatchString(line) {
			return true
		}
	}
	return false
}

// checkFlakiness compares the current failure against recent runs of the same workflow
func (c *Client) checkFlakiness(ctx context.Context, run *WorkflowRun, failedJobs []*FailedJob) *FlakinessInfo { //nolint:funlen,gocognit,gocyclo // The bounded comparison loop is clearer as one pass.
	info := &FlakinessInfo{}

	recentRuns, err := c.GetWorkflowRuns(ctx, run.WorkflowID, run.Branch)
	if err != nil {
		log.Debugf("Could not fetch recent runs for flakiness check: %v", err)
		info.Verdict = "unknown"
		return info
	}

	// Get the names of failed jobs for comparison
	failedJobNames := make(map[string]bool)
	for _, fj := range failedJobs {
		failedJobNames[fj.JobName] = true
	}

	maxCheck := 10
	sameFailures := 0
	successes := 0
	failures := 0
	checked := 0
	fingerprintBudget := 3

	for _, r := range recentRuns {
		if r.ID == run.ID || r.Status != "completed" {
			continue
		}
		if checked >= maxCheck {
			break
		}
		checked++
		sameCommit := run.HeadSHA != "" && r.HeadSHA == run.HeadSHA && r.Event == run.Event
		info.Samples = append(info.Samples, FlakinessSample{RunID: r.ID, HeadSHA: r.HeadSHA, Conclusion: r.Conclusion, SameCommit: sameCommit})
		if sameCommit {
			if r.Conclusion == "success" {
				info.SameSHASuccesses++
			}
			if r.Conclusion == "failure" {
				info.SameSHAFailures++
			}
		}

		switch r.Conclusion {
		case "success":
			successes++
		case "failure":
			failures++
			// Check if the same jobs failed
			jobs, err := c.GetWorkflowJobs(ctx, r.ID, "", 0)
			if err != nil {
				continue
			}
			if sameCommit && fingerprintBudget > 0 {
				comparisons := c.compareFailureFingerprints(ctx, jobs, failedJobs, &fingerprintBudget)
				info.Samples[len(info.Samples)-1].Fingerprints = comparisons
				for _, comparison := range comparisons {
					if comparison.Fingerprint != "" {
						info.FingerprintComparisons++
					}
					if comparison.Matches {
						info.MatchingFingerprints++
					}
				}
			}
			for _, j := range jobs {
				if j.Conclusion == "failure" && failedJobNames[j.Name] {
					sameFailures++
					break
				}
			}
		}
	}

	info.Basis = "Job names show recurrence only. Up to three historical job-log fingerprints are compared only at the same SHA and event; matching causes plus a success suggest nondeterminism, not a confirmed flaky test."
	info.RecentRuns = checked
	info.RecentFailures = failures
	info.RecentSuccesses = successes
	info.SameFailureCount = sameFailures

	switch {
	case checked == 0:
		info.Verdict = "unknown"
	case info.SameSHASuccesses > 0:
		info.Verdict = "likely_flake"
	case successes == 0 && failures > 0:
		info.Verdict = "unknown"
	case failures == 0:
		info.Verdict = "first_failure"
	default:
		info.Verdict = "unknown"
	}

	return info
}

// buildDiagnosisSummary creates a human-readable summary of the diagnosis
func (c *Client) buildDiagnosisSummary(d *FailureDiagnosis) string {
	var sb strings.Builder

	if len(d.FailedJobs) == 0 {
		fmt.Fprintf(&sb, "Run %d concluded as %s but no failed jobs found (may be cancelled or skipped).", d.RunID, d.Conclusion)
		return sb.String()
	}

	jobNames := make([]string, 0, len(d.FailedJobs))
	totalErrors := 0
	for _, fj := range d.FailedJobs {
		jobNames = append(jobNames, fj.JobName)
		totalErrors += len(fj.ErrorLines)
	}

	fmt.Fprintf(&sb, "%d failed job(s): %s. ", len(d.FailedJobs), strings.Join(jobNames, ", "))
	fmt.Fprintf(&sb, "%d evidence line(s) extracted from annotations/logs.", totalErrors)

	if d.Flakiness != nil {
		fmt.Fprintf(&sb, " Flakiness verdict: %s", d.Flakiness.Verdict)
		if d.Flakiness.Verdict == "likely_flake" {
			fmt.Fprintf(&sb, " (same job failed in %d of last %d runs, but %d succeeded).",
				d.Flakiness.SameFailureCount, d.Flakiness.RecentRuns, d.Flakiness.RecentSuccesses)
		} else {
			sb.WriteString(".")
		}
	}

	return sb.String()
}
