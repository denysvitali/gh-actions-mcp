package github

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"sort"
)

type ArtifactReadOptions struct {
	FilePattern   string
	MaxFileSize   int64
	ManifestOnly  bool
	SummaryOnly   bool
	Offset        int
	Limit         int
	MaxTotalBytes int64
}

type ArtifactReportSummary struct {
	Path     string   `json:"path"`
	Format   string   `json:"format"`
	Total    int      `json:"total"`
	Failures int      `json:"failures"`
	Examples []string `json:"examples,omitempty"`
	Warning  string   `json:"warning,omitempty"`
}

// GetArtifactContentWithOptions returns a stable page of matching files. Both
// the compressed archive and cumulative decoded contents have hard budgets.
func (c *Client) GetArtifactContentWithOptions(ctx context.Context, id int64, opts ArtifactReadOptions) (*ArtifactContent, error) {
	if opts.Offset < 0 {
		return nil, fmt.Errorf("artifact offset must be nonnegative")
	}
	if opts.FilePattern != "" {
		if _, err := filepath.Match(opts.FilePattern, ""); err != nil {
			return nil, fmt.Errorf("invalid file pattern %q: %w", opts.FilePattern, err)
		}
	}
	art, err := c.GetArtifactByID(ctx, id)
	if err != nil {
		return nil, err
	}
	archive, err := c.cachedArtifactArchive(ctx, id)
	if err != nil {
		return nil, err
	}
	selected := selectArtifactFiles(archive, opts.FilePattern)
	return artifactContentPage(ctx, art, selected, opts)
}

func selectArtifactFiles(archive *zip.Reader, pattern string) []*zip.File {
	selected := []*zip.File{}
	for _, file := range archive.File {
		if file.FileInfo().IsDir() {
			continue
		}
		if pattern != "" {
			matches, _ := filepath.Match(pattern, file.Name)
			if !matches {
				continue
			}
		}
		selected = append(selected, file)
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].Name < selected[j].Name })
	return selected
}

func artifactContentPage(ctx context.Context, art *Artifact, selected []*zip.File, opts ArtifactReadOptions) (*ArtifactContent, error) { //nolint:gocognit,nestif // File-page budgeting, continuation, and manifest selection share one bounded loop.
	result := &ArtifactContent{Name: art.Name, ID: art.ID, SizeInBytes: art.SizeInBytes, Files: []*ArtifactFile{}, FileCount: len(selected)}
	limit := opts.Limit
	if limit <= 0 {
		limit = 100
	}
	limit = min(limit, 500)
	budget := opts.MaxTotalBytes
	if budget <= 0 || budget > 4*1024*1024 {
		budget = 4 * 1024 * 1024
	}
	maxSize := opts.MaxFileSize
	if maxSize <= 0 {
		maxSize = 1024 * 1024
	}
	maxSize = min(maxSize, max(int64(1), (budget-4)*3/4))
	for i := opts.Offset; i < len(selected); i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(result.Files) >= limit {
			result.Truncated = true
			next := i
			result.NextOffset = &next
			break
		}
		file := selected[i]
		entry := &ArtifactFile{Path: file.Name, Size: int64(file.UncompressedSize64)}
		if !opts.ManifestOnly {
			// Base64 can expand binary contents by a third. Reserve that upper bound
			// before decoding, so a page stays bounded even for non-text artifacts.
			reserved := int64(file.UncompressedSize64)*4/3 + 4
			if file.UncompressedSize64 <= uint64(maxSize) && reserved > budget && len(result.Files) > 0 {
				result.Truncated = true
				next := i
				result.NextOffset = &next
				break
			}
			entry = artifactFileEntry(file, maxSize)
			if entry == nil {
				entry = &ArtifactFile{Path: file.Name, Size: int64(file.UncompressedSize64), Content: "(file could not be read)"}
			}
			budget -= int64(len(entry.Content))
			if opts.SummaryOnly {
				appendArtifactReport(result, entry, file.Name)
			}
		}
		result.Files = append(result.Files, entry)
	}
	return result, nil
}

func appendArtifactReport(result *ArtifactContent, entry *ArtifactFile, path string) {
	if summary := summarizeArtifactReport(path, entry.Content); summary != nil {
		result.Reports = append(result.Reports, *summary)
	}
	entry.Content = ""
	entry.Encoding = ""
}

// Artifact IDs identify immutable uploads. The metadata lookup at the call site
// rechecks availability and authorization before any cached bytes are exposed.
func (c *Client) cachedArtifactArchive(ctx context.Context, id int64) (*zip.Reader, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := archiveCacheKey{client: c, artifactID: id}
	if data, ok := lookupArchiveData(key); ok {
		return zip.NewReader(bytes.NewReader(data), int64(len(data)))
	}
	body, err := c.openArtifactArchive(ctx, id)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	data, err := io.ReadAll(io.LimitReader(body, maxArchiveBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxArchiveBytes {
		return nil, fmt.Errorf("artifact exceeds %d-byte download limit", maxArchiveBytes)
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("failed to open artifact archive: %w", err)
	}
	storeRunArchive(key, data)
	return archive, nil
}
