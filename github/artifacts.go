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

// GetArtifactContent downloads an artifact into memory and returns its entries,
// sorted by path, without touching the filesystem.
//
// filePattern, when non-empty, is a filepath.Match glob against the entry name;
// non-matching entries are omitted. maxFileSize (bytes, 0 for unlimited) caps
// individual entries: a larger entry is still listed, with a placeholder message
// instead of its content. Content is returned verbatim for text and
// base64-encoded for binary (Encoding says which). An entry that cannot be opened
// or read is logged and skipped rather than failing the whole call.
func (c *Client) GetArtifactContent(ctx context.Context, artifactID int64, filePattern string, maxFileSize int64) (*ArtifactContent, error) {
	artifact, err := c.GetArtifactByID(ctx, artifactID)
	if err != nil {
		return nil, err
	}

	body, err := c.openArtifactArchive(ctx, artifactID)
	if err != nil {
		return nil, err
	}
	defer body.Close()

	zipData, err := io.ReadAll(body)
	if err != nil {
		return nil, fmt.Errorf("failed to read artifact data: %w", err)
	}
	zipReader, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
	if err != nil {
		return nil, fmt.Errorf("failed to open artifact archive: %w", err)
	}

	files, err := artifactFilesFromZip(zipReader, filePattern, maxFileSize)
	if err != nil {
		return nil, err
	}

	return &ArtifactContent{
		Name:        artifact.Name,
		ID:          artifact.ID,
		SizeInBytes: artifact.SizeInBytes,
		Files:       files,
		FileCount:   len(files),
	}, nil
}

// openArtifactArchive resolves the artifact's pre-signed download URL and returns
// the response body. The caller must Close it.
//
// The pre-signed fetch deliberately uses presignedHTTPClient rather than the
// authenticated client: storage backends reject requests that carry an
// Authorization header alongside a pre-signed signature.
func (c *Client) openArtifactArchive(ctx context.Context, artifactID int64) (io.ReadCloser, error) {
	zipURL, resp, err := c.gh.Actions.DownloadArtifact(ctx, c.owner, c.repo, artifactID, maxRedirects)
	if err != nil {
		return nil, fmt.Errorf("failed to get artifact download URL: %w", err)
	}
	if resp != nil && resp.StatusCode != 0 {
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusFound {
			return nil, fmt.Errorf("failed to download artifact: HTTP %d", resp.StatusCode)
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, zipURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build artifact request: %w", err)
	}
	zipResp, err := presignedHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch artifact: %w", err)
	}
	if zipResp.StatusCode != http.StatusOK {
		_ = zipResp.Body.Close()
		return nil, fmt.Errorf("failed to fetch artifact: HTTP %d", zipResp.StatusCode)
	}
	return zipResp.Body, nil
}

// artifactFilesFromZip converts the archive entries matching filePattern into
// ArtifactFiles, sorted by path. An invalid pattern is the only error; unreadable
// entries are logged and dropped.
func artifactFilesFromZip(zipReader *zip.Reader, filePattern string, maxFileSize int64) ([]*ArtifactFile, error) {
	var files []*ArtifactFile
	for _, file := range zipReader.File {
		if file.FileInfo().IsDir() {
			continue
		}
		if filePattern != "" {
			matched, err := filepath.Match(filePattern, file.Name)
			if err != nil {
				return nil, fmt.Errorf("invalid file pattern %q: %w", filePattern, err)
			}
			if !matched {
				continue
			}
		}
		if entry := artifactFileEntry(file, maxFileSize); entry != nil {
			files = append(files, entry)
		}
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].Path < files[j].Path
	})
	return files, nil
}

// artifactFileEntry reads one archive entry. It returns nil when the entry cannot
// be opened or read — that is logged, not fatal, because one corrupt entry should
// not hide the rest of the artifact.
func artifactFileEntry(file *zip.File, maxFileSize int64) *ArtifactFile {
	size := int64(file.UncompressedSize64)

	if maxFileSize > 0 && file.UncompressedSize64 > uint64(maxFileSize) {
		return &ArtifactFile{
			Path:    file.Name,
			Size:    size,
			Content: fmt.Sprintf("(file too large to read, size: %d bytes)", file.UncompressedSize64),
		}
	}

	rc, err := file.Open()
	if err != nil {
		log.Debugf("Warning: could not open %s in artifact: %v", file.Name, err)
		return nil
	}
	content, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		log.Debugf("Warning: could not read %s in artifact: %v", file.Name, err)
		return nil
	}

	if !isTextContent(content) {
		return &ArtifactFile{
			Path:     file.Name,
			Size:     size,
			Content:  base64.StdEncoding.EncodeToString(content),
			Encoding: "base64",
		}
	}
	return &ArtifactFile{
		Path:     file.Name,
		Size:     size,
		Content:  string(content),
		Encoding: "text",
	}
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

// artifactDestination is a validated, opened download target. root confines every
// subsequent path operation, so a hostile OutputPath cannot escape it.
type artifactDestination struct {
	root *os.Root
	// rootPath is the absolute directory root refers to.
	rootPath string
	// outputPath is relative to rootPath and guaranteed local.
	outputPath string
}

// DownloadArtifactWithOptions writes an artifact beneath options.Root.
//
// The destination is validated and opened before anything is downloaded, so an
// escaping path or an existing file fails without spending bandwidth. The archive
// lands in a temporary file inside the root and is published by rename (with
// Overwrite) or by link (without), so a reader never observes a partial file.
// Without Overwrite an existing destination is an error and is left untouched.
func (c *Client) DownloadArtifactWithOptions(ctx context.Context, artifactID int64, options ArtifactDownloadOptions) (*ArtifactDownloadResult, error) {
	artifact, err := c.GetArtifactByID(ctx, artifactID)
	if err != nil {
		return nil, err
	}

	dest, err := openArtifactDestination(options, artifact.Name)
	if err != nil {
		return nil, err
	}
	defer dest.root.Close()

	body, err := c.openArtifactArchive(ctx, artifactID)
	if err != nil {
		return nil, err
	}
	defer body.Close()

	bytesWritten, fileCount, err := dest.publish(body, options.Overwrite)
	if err != nil {
		return nil, err
	}

	savedPath := filepath.Join(dest.rootPath, dest.outputPath)
	if options.Root == "" || options.Root == "." {
		savedPath = dest.outputPath
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

// openArtifactDestination resolves and opens the download target. An empty
// OutputPath is derived from artifactName with path separators flattened, so an
// artifact called "logs/nested" becomes "logs-nested.zip". The caller owns the
// returned root and must Close it.
func openArtifactDestination(options ArtifactDownloadOptions, artifactName string) (*artifactDestination, error) {
	outputPath := filepath.Clean(options.OutputPath)
	if options.OutputPath == "" {
		name := strings.NewReplacer("/", "-", "\\", "-").Replace(artifactName)
		outputPath = fmt.Sprintf("%s.zip", filepath.Base(name))
	}
	if !filepath.IsLocal(outputPath) {
		return nil, fmt.Errorf("output path %q must stay beneath artifact root", options.OutputPath)
	}

	rootPath := options.Root
	if rootPath == "" {
		rootPath = "."
	}
	rootPath, err := filepath.Abs(rootPath)
	if err != nil {
		return nil, fmt.Errorf("resolve artifact root: %w", err)
	}
	if err := os.MkdirAll(rootPath, 0o750); err != nil {
		return nil, fmt.Errorf("create artifact root: %w", err)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, fmt.Errorf("open artifact root: %w", err)
	}

	dest := &artifactDestination{root: root, rootPath: rootPath, outputPath: outputPath}
	if err := dest.prepare(options.Overwrite); err != nil {
		_ = root.Close()
		return nil, err
	}
	return dest, nil
}

// prepare creates the output directory and, unless overwrite is set, verifies
// that nothing already occupies the destination.
func (d *artifactDestination) prepare(overwrite bool) error {
	if err := d.root.MkdirAll(filepath.Dir(d.outputPath), 0o750); err != nil {
		return fmt.Errorf("create artifact output directory: %w", err)
	}
	if overwrite {
		return nil
	}
	_, statErr := d.root.Lstat(d.outputPath)
	switch {
	case statErr == nil:
		return fmt.Errorf("artifact destination %q already exists; set overwrite=true to replace it", d.outputPath)
	case !os.IsNotExist(statErr):
		return fmt.Errorf("inspect artifact destination %q: %w", d.outputPath, statErr)
	default:
		return nil
	}
}

// publish streams body into a temporary file inside the root, counts the archive
// entries, then moves it to outputPath. The temporary file is removed on every
// path, success or failure.
func (d *artifactDestination) publish(body io.Reader, overwrite bool) (bytesWritten int64, fileCount int, err error) {
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		return 0, 0, fmt.Errorf("create artifact temporary name: %w", err)
	}
	tempPath := filepath.Join(filepath.Dir(d.outputPath), "."+filepath.Base(d.outputPath)+".tmp-"+hex.EncodeToString(random[:]))

	outFile, err := d.root.OpenFile(tempPath, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return 0, 0, fmt.Errorf("create artifact temporary file: %w", err)
	}
	defer func() {
		_ = outFile.Close()
		_ = d.root.Remove(tempPath)
	}()

	bytesWritten, err = io.Copy(outFile, body)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to write artifact to file: %w", err)
	}
	if err := outFile.Sync(); err != nil {
		return 0, 0, fmt.Errorf("sync artifact temporary file: %w", err)
	}
	if _, err := outFile.Seek(0, 0); err != nil {
		return 0, 0, fmt.Errorf("failed to seek artifact file: %w", err)
	}

	// A payload that is not a readable ZIP is not an error: it is still saved,
	// it just reports zero entries.
	fileCount = countZipEntries(outFile, bytesWritten)

	if err := outFile.Close(); err != nil {
		return 0, 0, fmt.Errorf("close artifact temporary file: %w", err)
	}
	if err := d.move(tempPath, overwrite); err != nil {
		return 0, 0, err
	}
	return bytesWritten, fileCount, nil
}

// move publishes tempPath at outputPath. With overwrite it renames (replacing any
// existing file); without, it links, which fails rather than clobbering, then
// drops the temporary name.
func (d *artifactDestination) move(tempPath string, overwrite bool) error {
	if overwrite {
		if err := d.root.Rename(tempPath, d.outputPath); err != nil {
			return fmt.Errorf("publish artifact %q: %w", d.outputPath, err)
		}
		return nil
	}
	if err := d.root.Link(tempPath, d.outputPath); err != nil {
		return fmt.Errorf("publish artifact %q without overwrite: %w", d.outputPath, err)
	}
	if err := d.root.Remove(tempPath); err != nil {
		return fmt.Errorf("remove artifact temporary file: %w", err)
	}
	return nil
}

// countZipEntries counts the non-directory entries in the archive, returning 0 if
// the payload cannot be read as a ZIP.
func countZipEntries(reader io.ReaderAt, size int64) int {
	zipReader, err := zip.NewReader(reader, size)
	if err != nil {
		return 0
	}
	count := 0
	for _, file := range zipReader.File {
		if !file.FileInfo().IsDir() {
			count++
		}
	}
	return count
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
