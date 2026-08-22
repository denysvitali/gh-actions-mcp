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
	RunID      int64          `json:"run_id"`
	RunName    string         `json:"run_name"`
	RunURL     string         `json:"run_url"`
	Branch     string         `json:"branch"`
	HeadSHA    string         `json:"head_sha"`
	Conclusion string         `json:"conclusion"`
	FailedJobs []*FailedJob   `json:"failed_jobs"`
	Flakiness  *FlakinessInfo `json:"flakiness,omitempty"`
	Summary    string         `json:"summary"`
}

// FailedJob represents a job that failed within a workflow run
type FailedJob struct {
	JobID       int64         `json:"job_id"`
	JobName     string        `json:"job_name"`
	Conclusion  string        `json:"conclusion"`
	FailedSteps []*FailedStep `json:"failed_steps"`
	ErrorLines  []string      `json:"error_lines"`
	ErrorSource string        `json:"error_source,omitempty"`
}

// FailedStep represents a step that failed within a job
type FailedStep struct {
	Name       string `json:"name"`
	Number     int64  `json:"number"`
	Conclusion string `json:"conclusion"`
}

// FlakinessInfo contains information about whether this failure is likely a flake
type FlakinessInfo struct {
	RecentRuns       int    `json:"recent_runs_checked"`
	RecentFailures   int    `json:"recent_failures"`
	RecentSuccesses  int    `json:"recent_successes"`
	SameFailureCount int    `json:"same_failure_count"`
	Verdict          string `json:"verdict"` // "likely_flake", "likely_regression", "first_failure", "unknown"
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
		RunName:    run.Name,
		RunURL:     run.URL,
		Branch:     run.Branch,
		HeadSHA:    run.HeadSHA,
		Conclusion: run.Conclusion,
	}

	if run.Status != "completed" {
		diagnosis.Summary = fmt.Sprintf("Run %d is still %s (not completed yet)", runID, run.Status)
		return diagnosis, nil
	}

	if run.Conclusion == "success" {
		diagnosis.Summary = fmt.Sprintf("Run %d succeeded — nothing to diagnose", runID)
		return diagnosis, nil
	}

	// 2. Get jobs and identify failures
	jobs, err := c.GetWorkflowJobs(ctx, runID, "", 0)
	if err != nil {
		return nil, fmt.Errorf("failed to get jobs for run %d: %w", runID, err)
	}

	for _, job := range jobs {
		if job.Conclusion != "failure" && job.Conclusion != "cancelled" && job.Conclusion != "timed_out" {
			continue
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

		// Check-run annotations contain structured, actionable failures without
		// downloading a potentially large log archive. Logs remain the fallback
		// because Actions does not emit annotations for every failure.
		if errorLines := c.getCheckRunAnnotationErrors(ctx, job.ID, maxLogLines); len(errorLines) > 0 {
			failedJob.ErrorLines = errorLines
			failedJob.ErrorSource = "check_annotations"
		} else {
			failedJob.ErrorLines = c.extractErrorLines(ctx, runID, job.ID, maxLogLines)
			failedJob.ErrorSource = "logs"
		}

		diagnosis.FailedJobs = append(diagnosis.FailedJobs, failedJob)
	}

	// 4. Optional flakiness check
	if checkFlakiness && run.WorkflowID > 0 {
		diagnosis.Flakiness = c.checkFlakiness(ctx, run, diagnosis.FailedJobs)
	}

	// 5. Build summary
	diagnosis.Summary = c.buildDiagnosisSummary(diagnosis)

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
	lines := strings.Split(logs, "\n")
	annotations := collectMatchingErrorLines(lines, maxLines, true)
	if len(annotations) > 0 {
		return annotations
	}
	return collectMatchingErrorLines(lines, maxLines, false)
}

func collectMatchingErrorLines(lines []string, maxLines int, annotationsOnly bool) []string {
	var errorLines []string
	seen := make(map[string]bool)
	for _, line := range lines {
		cleaned := normalizeErrorLine(line)
		if cleaned == "" || seen[cleaned] {
			continue
		}
		if !isErrorLine(cleaned, annotationsOnly) {
			continue
		}
		seen[cleaned] = true
		errorLines = append(errorLines, cleaned)
		if len(errorLines) >= maxLines {
			break
		}
	}
	return errorLines
}

// normalizeErrorLine strips ANSI, GitHub timestamps, and surrounding
// whitespace. Empty after stripping means "ignore this line".
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

	for _, r := range recentRuns {
		if r.ID == run.ID || r.Status != "completed" {
			continue
		}
		checked++
		if checked > maxCheck {
			break
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
			for _, j := range jobs {
				if j.Conclusion == "failure" && failedJobNames[j.Name] {
					sameFailures++
					break
				}
			}
		}
	}

	info.RecentRuns = checked
	info.RecentFailures = failures
	info.RecentSuccesses = successes
	info.SameFailureCount = sameFailures

	switch {
	case checked == 0:
		info.Verdict = "unknown"
	case sameFailures >= 2 && successes > 0:
		info.Verdict = "likely_flake"
	case successes == 0 && failures > 0:
		info.Verdict = "likely_regression"
	case failures == 0:
		info.Verdict = "first_failure"
	default:
		info.Verdict = "likely_regression"
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
	fmt.Fprintf(&sb, "%d error line(s) extracted from logs.", totalErrors)

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
