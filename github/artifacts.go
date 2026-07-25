package github

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/google/go-github/v89/github"
)

// Artifact represents a workflow run artifact
type Artifact struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	SizeInBytes int64  `json:"size_in_bytes"`
	CreatedAt   string `json:"created_at"`
	ExpiresAt   string `json:"expires_at,omitempty"`
	ArchiveURL  string `json:"archive_url,omitempty"`
}

// ArtifactFile represents a single file within an artifact
type ArtifactFile struct {
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	Content  string `json:"content,omitempty"`
	Encoding string `json:"encoding,omitempty"` // "text" or "base64"
}

// ArtifactContent represents the contents of an artifact
type ArtifactContent struct {
	Name        string          `json:"name"`
	ID          int64           `json:"id"`
	SizeInBytes int64           `json:"size_in_bytes"`
	Files       []*ArtifactFile `json:"files"`
	FileCount   int             `json:"file_count"`
}

// ArtifactDownloadResult represents the result of downloading an artifact
type ArtifactDownloadResult struct {
	Name      string `json:"name"`
	ID        int64  `json:"id"`
	SavedPath string `json:"saved_path"`
	FileCount int    `json:"file_count"`
	TotalSize int64  `json:"total_size"`
}

// GetWorkflowRunArtifacts retrieves artifacts for a workflow run
func (c *Client) GetWorkflowRunArtifacts(ctx context.Context, runID int64) ([]*Artifact, error) {
	arts, _, err := c.gh.Actions.ListWorkflowRunArtifacts(ctx, c.owner, c.repo, runID, &github.ListOptions{
		PerPage: c.perPageLimit,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list artifacts for run %d: %w", runID, err)
	}

	result := make([]*Artifact, 0, len(arts.Artifacts))
	for _, art := range arts.Artifacts {
		result = append(result, &Artifact{
			ID:          art.GetID(),
			Name:        art.GetName(),
			SizeInBytes: art.GetSizeInBytes(),
			CreatedAt:   formatTimeValue(art.GetCreatedAt()),
			ExpiresAt:   formatTimeValue(art.GetExpiresAt()),
			ArchiveURL:  art.GetArchiveDownloadURL(),
		})
	}

	return result, nil
}

// GetArtifactByID retrieves a single artifact by its ID
func (c *Client) GetArtifactByID(ctx context.Context, artifactID int64) (*Artifact, error) {
	art, _, err := c.gh.Actions.GetArtifact(ctx, c.owner, c.repo, artifactID)
	if err != nil {
		return nil, fmt.Errorf("failed to get artifact %d: %w", artifactID, err)
	}

	return &Artifact{
		ID:          art.GetID(),
		Name:        art.GetName(),
		SizeInBytes: art.GetSizeInBytes(),
		CreatedAt:   formatTimeValue(art.GetCreatedAt()),
		ExpiresAt:   formatTimeValue(art.GetExpiresAt()),
		ArchiveURL:  art.GetArchiveDownloadURL(),
	}, nil
}

// GetArtifactContent retrieves the contents of an artifact without downloading to disk
// If filePattern is provided, only files matching the pattern will be returned
// maxFileSize limits the size of individual files read (in bytes, 0 for unlimited)
// For text files, content is returned as a string. For binary files, content is base64 encoded.
func (c *Client) GetArtifactContent(ctx context.Context, artifactID int64, filePattern string, maxFileSize int64) (*ArtifactContent, error) {
	// First get artifact metadata
	artifact, err := c.GetArtifactByID(ctx, artifactID)
	if err != nil {
		return nil, err
	}

	// Download the artifact ZIP
	zipURL, resp, err := c.gh.Actions.DownloadArtifact(ctx, c.owner, c.repo, artifactID, maxRedirects)
	if err != nil {
		return nil, fmt.Errorf("failed to get artifact download URL: %w", err)
	}

	if resp != nil && resp.StatusCode != 0 {
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusFound {
			return nil, fmt.Errorf("failed to download artifact: HTTP %d", resp.StatusCode)
		}
	}

	// Fetch the ZIP from the pre-signed URL without auth headers.
	// Storage backends reject Authorization headers on pre-signed URLs.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, zipURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build artifact request: %w", err)
	}
	zipResp, err := presignedHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch artifact: %w", err)
	}
	defer zipResp.Body.Close()

	if zipResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch artifact: HTTP %d", zipResp.StatusCode)
	}

	// Read the ZIP data
	zipData, err := io.ReadAll(zipResp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read artifact data: %w", err)
	}

	// Open the ZIP archive
	zipReader, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
	if err != nil {
		return nil, fmt.Errorf("failed to open artifact archive: %w", err)
	}

	// Process files in the ZIP
	var files []*ArtifactFile
	var totalSize int64

	for _, file := range zipReader.File {
		if file.FileInfo().IsDir() {
			continue
		}

		// Apply file pattern filter if specified
		if filePattern != "" {
			matched, err := filepath.Match(filePattern, file.Name)
			if err != nil {
				return nil, fmt.Errorf("invalid file pattern %q: %w", filePattern, err)
			}
			if !matched {
				continue
			}
		}

		// Skip files larger than maxFileSize (if specified)
		if maxFileSize > 0 && file.UncompressedSize64 > uint64(maxFileSize) {
			files = append(files, &ArtifactFile{
				Path:    file.Name,
				Size:    int64(file.UncompressedSize64),
				Content: fmt.Sprintf("(file too large to read, size: %d bytes)", file.UncompressedSize64),
			})
			totalSize += int64(file.UncompressedSize64)
			continue
		}

		// Read file content
		rc, err := file.Open()
		if err != nil {
			log.Debugf("Warning: could not open %s in artifact: %v", file.Name, err)
			continue
		}

		content, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			log.Debugf("Warning: could not read %s in artifact: %v", file.Name, err)
			continue
		}

		totalSize += int64(file.UncompressedSize64)

		// Detect if content is text or binary
		encoding := "text"
		contentStr := string(content)
		if !isTextContent(content) {
			encoding = "base64"
			contentStr = base64.StdEncoding.EncodeToString(content)
		}

		files = append(files, &ArtifactFile{
			Path:     file.Name,
			Size:     int64(file.UncompressedSize64),
			Content:  contentStr,
			Encoding: encoding,
		})
	}

	// Sort files by path
	sort.Slice(files, func(i, j int) bool {
		return files[i].Path < files[j].Path
	})

	return &ArtifactContent{
		Name:        artifact.Name,
		ID:          artifact.ID,
		SizeInBytes: artifact.SizeInBytes,
		Files:       files,
		FileCount:   len(files),
	}, nil
}

type ArtifactDownloadOptions struct {
	Root       string
	OutputPath string
	Overwrite  bool
}

// DownloadArtifact preserves the library API while using safe, atomic writes.
func (c *Client) DownloadArtifact(ctx context.Context, artifactID int64, outputPath string) (*ArtifactDownloadResult, error) {
	root := "."
	relativePath := outputPath
	if filepath.IsAbs(outputPath) {
		root = filepath.Dir(outputPath)
		relativePath = filepath.Base(outputPath)
	}
	return c.DownloadArtifactWithOptions(ctx, artifactID, ArtifactDownloadOptions{Root: root, OutputPath: relativePath})
}

// DownloadArtifactWithOptions writes an artifact beneath Root using a
// temporary file and atomic publication. Existing files are preserved unless
// Overwrite is explicitly set.
func (c *Client) DownloadArtifactWithOptions(ctx context.Context, artifactID int64, options ArtifactDownloadOptions) (*ArtifactDownloadResult, error) {
	// First get artifact metadata
	artifact, err := c.GetArtifactByID(ctx, artifactID)
	if err != nil {
		return nil, err
	}

	// Generate default output path if not provided
	outputPath := filepath.Clean(options.OutputPath)
	if options.OutputPath == "" {
		name := strings.NewReplacer("/", "-", "\\", "-").Replace(artifact.Name)
		outputPath = fmt.Sprintf("%s.zip", filepath.Base(name))
	}
	if !filepath.IsLocal(outputPath) {
		return nil, fmt.Errorf("output path %q must stay beneath artifact root", options.OutputPath)
	}
	rootPath := options.Root
	if rootPath == "" {
		rootPath = "."
	}
	rootPath, err = filepath.Abs(rootPath)
	if err != nil {
		return nil, fmt.Errorf("resolve artifact root: %w", err)
	}
	if err := os.MkdirAll(rootPath, 0750); err != nil {
		return nil, fmt.Errorf("create artifact root: %w", err)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, fmt.Errorf("open artifact root: %w", err)
	}
	defer root.Close()
	if err := root.MkdirAll(filepath.Dir(outputPath), 0750); err != nil {
		return nil, fmt.Errorf("create artifact output directory: %w", err)
	}
	if !options.Overwrite {
		if _, statErr := root.Lstat(outputPath); statErr == nil {
			return nil, fmt.Errorf("artifact destination %q already exists; set overwrite=true to replace it", outputPath)
		} else if !os.IsNotExist(statErr) {
			return nil, fmt.Errorf("inspect artifact destination %q: %w", outputPath, statErr)
		}
	}

	// Download the artifact ZIP
	zipURL, resp, err := c.gh.Actions.DownloadArtifact(ctx, c.owner, c.repo, artifactID, maxRedirects)
	if err != nil {
		return nil, fmt.Errorf("failed to get artifact download URL: %w", err)
	}

	if resp != nil && resp.StatusCode != 0 {
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusFound {
			return nil, fmt.Errorf("failed to download artifact: HTTP %d", resp.StatusCode)
		}
	}

	// Fetch the ZIP from the pre-signed URL without auth headers.
	// Storage backends reject Authorization headers on pre-signed URLs.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, zipURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build artifact request: %w", err)
	}
	zipResp, err := presignedHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch artifact: %w", err)
	}
	defer zipResp.Body.Close()

	if zipResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch artifact: HTTP %d", zipResp.StatusCode)
	}

	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, fmt.Errorf("create artifact temporary name: %w", err)
	}
	tempPath := filepath.Join(filepath.Dir(outputPath), "."+filepath.Base(outputPath)+".tmp-"+hex.EncodeToString(random[:]))
	outFile, err := root.OpenFile(tempPath, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, fmt.Errorf("create artifact temporary file: %w", err)
	}
	defer func() {
		_ = outFile.Close()
		_ = root.Remove(tempPath)
	}()

	// Copy data to file
	bytesWritten, err := io.Copy(outFile, zipResp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to write artifact to file: %w", err)
	}
	if err := outFile.Sync(); err != nil {
		return nil, fmt.Errorf("sync artifact temporary file: %w", err)
	}

	// Count files in the archive
	if _, err := outFile.Seek(0, 0); err != nil {
		return nil, fmt.Errorf("failed to seek artifact file: %w", err)
	}
	zipReader, err := zip.NewReader(outFile, bytesWritten)
	fileCount := 0
	if err == nil {
		for _, file := range zipReader.File {
			if !file.FileInfo().IsDir() {
				fileCount++
			}
		}
	}
	if err := outFile.Close(); err != nil {
		return nil, fmt.Errorf("close artifact temporary file: %w", err)
	}
	if options.Overwrite {
		if err := root.Rename(tempPath, outputPath); err != nil {
			return nil, fmt.Errorf("publish artifact %q: %w", outputPath, err)
		}
	} else {
		if err := root.Link(tempPath, outputPath); err != nil {
			return nil, fmt.Errorf("publish artifact %q without overwrite: %w", outputPath, err)
		}
		if err := root.Remove(tempPath); err != nil {
			return nil, fmt.Errorf("remove artifact temporary file: %w", err)
		}
	}

	savedPath := filepath.Join(rootPath, outputPath)
	if options.Root == "" || options.Root == "." {
		savedPath = outputPath
	}
	log.Infof("Downloaded artifact %q to %s (%d bytes, %d files)", artifact.Name, savedPath, bytesWritten, fileCount)

	return &ArtifactDownloadResult{
		Name:      artifact.Name,
		ID:        artifact.ID,
		SavedPath: savedPath,
		FileCount: fileCount,
		TotalSize: bytesWritten,
	}, nil
}

// isTextContent attempts to detect if content is text or binary
func isTextContent(data []byte) bool {
	if len(data) == 0 {
		return true
	}

	// Check first 512 bytes for null bytes (indicates binary)
	sampleSize := 512
	if len(data) < sampleSize {
		sampleSize = len(data)
	}

	for i := 0; i < sampleSize; i++ {
		if data[i] == 0 {
			return false
		}
	}

	// Check for common text file extensions or content patterns
	return true
}
