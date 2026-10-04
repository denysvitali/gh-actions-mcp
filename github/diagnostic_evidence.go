package github

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"sort"
	"strings"
)

const maxDiagnosticBytes = 32 * 1024

var causePattern = regexp.MustCompile(`(?i)(GO-\d{4}-\d+|CVE-\d{4}-\d+|GHSA-[a-z0-9-]+|fixed in:|found in:|vulnerability|assertion|expected:|actual:|undefined:|cannot find|test result:)`)

func diagnosticLogEvidence(jobID int64, logs string, limit int) []DiagnosticEvidence {
	lines := strings.Split(logs, "\n")
	selected := diagnosticLineIndices(lines)
	result := make([]DiagnosticEvidence, 0)
	used := 0
	for i, line := range lines {
		if !selected[i] {
			continue
		}
		clean := normalizeErrorLine(line)
		if clean == "" || scriptSourceLine.MatchString(clean) {
			continue
		}
		if len(result) >= limit || used >= maxDiagnosticBytes {
			break
		}
		if len(clean) > maxDiagnosticBytes-used {
			clean = clean[:maxDiagnosticBytes-used]
		}
		used += len(clean)
		result = append(result, DiagnosticEvidence{Source: "logs", JobID: jobID, Line: i + 1, Text: clean})
	}
	return result
}

func mergeDiagnosticEvidence(jobID int64, annotations []string, logs string, limit int) ([]string, []DiagnosticEvidence) {
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, 200)
	// Reserve most of the budget for logs: one generic annotation must never crowd
	// out a concrete vulnerability identifier or compiler error.
	evidence := diagnosticLogEvidence(jobID, logs, limit)
	for _, line := range annotations {
		evidence = append(evidence, DiagnosticEvidence{Source: "check_annotations", JobID: jobID, Text: line})
	}
	lines := []string{}
	kept := []DiagnosticEvidence{}
	seen := map[string]bool{}
	used := 0
	for _, item := range evidence {
		if seen[item.Text] {
			continue
		}
		if len(kept) >= limit || used >= maxDiagnosticBytes {
			break
		}
		if len(item.Text) > maxDiagnosticBytes-used {
			item.Text = item.Text[:maxDiagnosticBytes-used]
		}
		used += len(item.Text)
		seen[item.Text] = true
		lines = append(lines, item.Text)
		kept = append(kept, item)
	}
	return lines, kept
}

func diagnosticFingerprint(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	normalized := make([]string, 0, len(lines))
	seen := map[string]bool{}
	for _, line := range lines {
		clean := normalizeErrorLine(line)
		if clean == "" || strings.Contains(strings.ToLower(clean), "process completed with exit code") {
			continue
		}
		if !causePattern.MatchString(clean) && !isErrorLine(clean, false) {
			continue
		}
		if !seen[clean] {
			normalized = append(normalized, clean)
			seen[clean] = true
		}
	}
	if len(normalized) == 0 {
		return ""
	}
	sort.Strings(normalized)
	sum := sha256.Sum256([]byte(strings.Join(normalized, "\n")))
	return hex.EncodeToString(sum[:8])
}

func diagnosticLineIndices(lines []string) map[int]bool {
	selected := map[int]bool{}
	// Preserve root-cause matches alongside runner annotations. Keep a small
	// context window so a test's expected/actual values survive extraction.
	for i, line := range lines {
		clean := normalizeErrorLine(line)
		if clean != "" && (causePattern.MatchString(clean) || isErrorLine(clean, false)) && !scriptSourceLine.MatchString(clean) {
			for j := max(0, i-1); j <= min(len(lines)-1, i+2); j++ {
				selected[j] = true
			}
		}
	}
	return selected
}

// FailureFingerprintComparison records both comparison evidence and unavailable
// logs, so an absent match is never confused with a different failure.
type FailureFingerprintComparison struct {
	JobID       int64  `json:"job_id"`
	JobName     string `json:"job_name"`
	Fingerprint string `json:"fingerprint,omitempty"`
	Matches     bool   `json:"matches"`
	Warning     string `json:"warning,omitempty"`
}

func (c *Client) compareFailureFingerprints(ctx context.Context, jobs []*Job, current []*FailedJob, budget *int) []FailureFingerprintComparison {
	wanted := make(map[string]string)
	for _, job := range current {
		if job.Fingerprint != "" {
			wanted[job.JobName] = job.Fingerprint
		}
	}
	var result []FailureFingerprintComparison
	for _, job := range jobs {
		fingerprint, ok := wanted[job.Name]
		if !ok || job.Conclusion != "failure" {
			continue
		}
		if *budget <= 0 {
			break
		}
		*budget--
		comparison := FailureFingerprintComparison{JobID: job.ID, JobName: job.Name}
		logs, err := c.jobLogs(ctx, job.ID, LogViewOptions{NoHeaders: true})
		if err != nil {
			comparison.Warning = "Historical job logs unavailable"
		} else {
			comparison.Fingerprint = diagnosticFingerprint(collectErrorLines(logs, 50))
			comparison.Matches = comparison.Fingerprint != "" && comparison.Fingerprint == fingerprint
		}
		result = append(result, comparison)
	}
	return result
}
