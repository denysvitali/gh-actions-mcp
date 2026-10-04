package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/denysvitali/gh-actions-mcp/github"
)

// artifactEvidence preserves the existing artifact fields while adding bounded
// continuation. FileByteOffset applies to the first returned file's content.
type artifactEvidence struct {
	github.ArtifactContent
	FileOffset     int    `json:"file_offset"`
	FileByteOffset int    `json:"file_byte_offset,omitempty"`
	ReturnedBytes  int    `json:"returned_bytes"`
	ReturnedLines  int    `json:"returned_lines"`
	NextCursor     string `json:"next_cursor,omitempty"`
}

func (s *MCPServer) readArtifactEvidence(ctx context.Context, client *github.Client, owner, repo string, input artifactInput) (artifactEvidence, error) {
	var result artifactEvidence
	bytes, lines, err := evidenceBudgets(input.evidenceInput)
	if err != nil {
		return result, err
	}
	maxSize := input.MaxFileSize
	if maxSize <= 0 {
		maxSize = defaultMaxArtifactFileSize
	}
	scope := struct {
		Kind, Owner, Repo, APIBaseURL, AuthUsername string
		ID                                          int64
		Pattern                                     string
		MaxFileSize                                 int64
		Manifest, Summary                           bool
	}{"artifact", owner, repo, s.config.APIBaseURL, s.config.AuthUsername, input.ArtifactID, input.FilePattern, maxSize, input.ManifestOnly, input.SummaryOnly}
	position, err := decodeCursor(input.Cursor, scope, s.cursorKey())
	if err != nil {
		return result, err
	}
	content, err := client.GetArtifactContentWithOptions(ctx, input.ArtifactID, github.ArtifactReadOptions{FilePattern: input.FilePattern, MaxFileSize: maxSize, ManifestOnly: input.ManifestOnly, SummaryOnly: input.SummaryOnly, Offset: position.Page - 1, Limit: 100})
	if err != nil {
		return result, err
	}
	prepareReportEvidence(content)
	result, next, err := artifactEvidencePage(content, position, bytes, lines)
	if err != nil {
		return result, err
	}
	if next.Page == 0 && content.NextOffset != nil {
		next.Page = *content.NextOffset + 1
	}
	projectReportEvidence(&result, content.Reports)
	result.NextOffset = nil
	result.Truncated = next.Page > 0
	if result.Truncated {
		result.NextCursor, err = encodeCursor(next, scope, s.cursorKey())
		if err != nil {
			return result, err
		}
	}
	return result, nil
}

func artifactEvidencePage(content *github.ArtifactContent, position cursorPosition, bytes, lines int) (artifactEvidence, cursorPosition, error) {
	result := artifactEvidence{ArtifactContent: *content}
	result.Files = nil
	result.FileOffset = position.Page - 1
	result.FileByteOffset = position.Offset
	next := cursorPosition{}
	remainingBytes, remainingLines := bytes, lines
	for i, file := range content.Files {
		offset := 0
		if i == 0 {
			offset = position.Offset
		}
		if offset > len(file.Content) {
			return result, cursorPosition{}, fmt.Errorf("invalid artifact content cursor offset")
		}
		if remainingBytes <= 0 || remainingLines <= 0 {
			next = cursorPosition{Page: position.Page + i, Offset: offset}
			break
		}
		copyFile := *file
		copyFile.Content = evidenceChunk(file.Content[offset:], remainingBytes, remainingLines)
		result.Files = append(result.Files, &copyFile)
		size, count := len(copyFile.Content), evidenceLineCount(copyFile.Content)
		result.ReturnedBytes += size
		result.ReturnedLines += count
		remainingBytes -= size
		remainingLines -= count
		if offset+size < len(file.Content) {
			next = cursorPosition{Page: position.Page + i, Offset: offset + size}
			break
		}
	}
	return result, next, nil
}

// Report examples use the same byte continuation as file contents, so summary
// mode cannot bypass response budgets through a large matrix of failed tests.
func prepareReportEvidence(content *github.ArtifactContent) {
	reports := make(map[string]github.ArtifactReportSummary, len(content.Reports))
	for _, report := range content.Reports {
		reports[report.Path] = report
	}
	for _, file := range content.Files {
		if report, ok := reports[file.Path]; ok {
			file.Content = strings.Join(report.Examples, "\n")
		}
	}
}

func projectReportEvidence(result *artifactEvidence, reports []github.ArtifactReportSummary) {
	if len(reports) == 0 {
		return
	}
	byPath := make(map[string]github.ArtifactReportSummary, len(reports))
	for _, report := range reports {
		byPath[report.Path] = report
	}
	result.Reports = nil
	for _, file := range result.Files {
		if report, ok := byPath[file.Path]; ok {
			report.Examples = nil
			if file.Content != "" {
				report.Examples = strings.Split(file.Content, "\n")
			}
			result.Reports = append(result.Reports, report)
			file.Content = ""
		}
	}
}
