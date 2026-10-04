package github

import (
	"encoding/json"
	"encoding/xml"
	"strings"
)

type (
	junitFailure struct {
		Message string `xml:"message,attr"`
		Text    string `xml:",chardata"`
	}
	junitCase struct {
		Name    string        `xml:"name,attr"`
		Failure *junitFailure `xml:"failure"`
		Error   *junitFailure `xml:"error"`
	}
	junitSuite struct {
		Cases  []junitCase  `xml:"testcase"`
		Suites []junitSuite `xml:"testsuite"`
	}
)

func summarizeArtifactReport(path, content string) *ArtifactReportSummary {
	if len(content) > 1024*1024 {
		return &ArtifactReportSummary{Path: path, Warning: "report exceeds summary limit"}
	}
	lower := strings.ToLower(path)
	switch {
	case strings.HasSuffix(lower, ".xml"):
		return summarizeJUnit(path, content)
	case strings.HasSuffix(lower, ".sarif"), strings.HasSuffix(lower, ".sarif.json"):
		return summarizeSARIF(path, content)
	default:
		return nil
	}
}

func summarizeJUnit(path, content string) *ArtifactReportSummary {
	result := &ArtifactReportSummary{Path: path, Format: "junit"}
	var suite junitSuite
	if err := xml.Unmarshal([]byte(content), &suite); err != nil {
		result.Warning = "invalid XML report"
		return result
	}
	accumulateJUnit(result, suite)
	return result
}

func accumulateJUnit(result *ArtifactReportSummary, suite junitSuite) {
	for _, item := range suite.Cases {
		result.Total++
		failure := item.Failure
		if item.Error != nil {
			failure = item.Error
		}
		if failure == nil {
			continue
		}
		result.Failures++
		if len(result.Examples) < 20 {
			message := failure.Message
			if message == "" {
				message = failure.Text
			}
			result.Examples = append(result.Examples, boundedReportText(item.Name+": "+message))
		}
	}
	for _, child := range suite.Suites {
		accumulateJUnit(result, child)
	}
}

func summarizeSARIF(path, content string) *ArtifactReportSummary {
	var report struct {
		Runs []struct {
			Results []struct {
				RuleID  string `json:"ruleId"`
				Message struct {
					Text string `json:"text"`
				} `json:"message"`
			} `json:"results"`
		} `json:"runs"`
	}
	result := &ArtifactReportSummary{Path: path, Format: "sarif"}
	if err := json.Unmarshal([]byte(content), &report); err != nil {
		result.Warning = "invalid SARIF report"
		return result
	}
	for _, run := range report.Runs {
		for _, item := range run.Results {
			result.Total++
			if len(result.Examples) < 20 {
				result.Examples = append(result.Examples, boundedReportText(item.RuleID+": "+item.Message.Text))
			}
		}
	}
	return result
}

func boundedReportText(s string) string {
	if len(s) > 512 {
		return s[:512] + "…"
	}
	return s
}
