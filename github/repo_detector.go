package github

import (
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/sirupsen/logrus"
)

const (
	DefaultRemoteName = "origin"
)

var (
	detectorLog *logrus.Logger
	once        sync.Once
)

// SetLogger sets the logger for the repo detector
func SetDetectorLogger(l *logrus.Logger) {
	once.Do(func() {
		detectorLog = l
	})
}

// RepoInfo contains information about a repository
type RepoInfo struct {
	Owner   string `json:"owner"`
	Repo    string `json:"repo"`
	Source  string `json:"source"`  // How the repo was detected (e.g., "config", "git_remote")
	Cached  bool   `json:"cached"`  // Whether this was from cache
	RawURL  string `json:"raw_url"` // Original URL if from git remote
}

// RepoDetector handles repository detection with caching
type RepoDetector struct {
	mu    sync.RWMutex
	cache *RepoInfo
}

// NewRepoDetector creates a new repository detector
func NewRepoDetector() *RepoDetector {
	return &RepoDetector{}
}

// ParseGitURL parses a git URL and extracts owner/repo
// Supports SSH, HTTPS, git://, and bare formats
func ParseGitURL(remoteURL string) (string, string, error) {
	// Validate URL - reject tokens
	if containsToken(remoteURL) {
		return "", "", fmt.Errorf("URL appears to contain a token (refusing for security)")
	}

	// Handle bare owner/repo format (e.g., "owner/repo")
	if !strings.Contains(remoteURL, "://") && !strings.Contains(remoteURL, "@") {
		path := strings.TrimSuffix(remoteURL, ".git")
		repoParts := strings.Split(path, "/")
		if len(repoParts) == 2 {
			return repoParts[0], repoParts[1], nil
		}
	}

	// Handle SSH format: git@github.com:owner/repo.git
	// Also handles malformed URLs like git@github.com:/owner/repo.git (extra slash)
	if strings.Contains(remoteURL, "git@") {
		parts := strings.Split(remoteURL, ":")
		if len(parts) > 1 {
			path := strings.TrimSuffix(parts[1], ".git")
			path = strings.TrimPrefix(path, "/") // Handle extra leading slash
			repoParts := strings.Split(path, "/")
			if len(repoParts) == 2 {
				return repoParts[0], repoParts[1], nil
			}
		}
	}

	// Handle HTTPS format: https://github.com/owner/repo.git
	if strings.HasPrefix(remoteURL, "https://") || strings.HasPrefix(remoteURL, "http://") {
		u, err := url.Parse(remoteURL)
		if err != nil {
			return "", "", fmt.Errorf("failed to parse URL: %w", err)
		}

		// Validate it's a GitHub URL
		if !isGitHubURL(u) {
			return "", "", fmt.Errorf("not a GitHub URL: %s", remoteURL)
		}

		path := strings.TrimPrefix(u.Path, "/")
		path = strings.TrimSuffix(path, ".git")
		repoParts := strings.Split(path, "/")
		if len(repoParts) >= 2 {
			return repoParts[0], repoParts[1], nil
		}

		return "", "", fmt.Errorf("could not extract owner/repo from URL: %s", remoteURL)
	}

	// Handle git:// protocol: git://github.com/owner/repo.git
	if strings.HasPrefix(remoteURL, "git://") {
		u, err := url.Parse(remoteURL)
		if err != nil {
			return "", "", fmt.Errorf("failed to parse git:// URL: %w", err)
		}

		// Validate it's a GitHub URL
		if !isGitHubURL(u) {
			return "", "", fmt.Errorf("not a GitHub URL: %s", remoteURL)
		}

		path := strings.TrimPrefix(u.Path, "/")
		path = strings.TrimSuffix(path, ".git")
		repoParts := strings.Split(path, "/")
		if len(repoParts) >= 2 {
			return repoParts[0], repoParts[1], nil
		}

		return "", "", fmt.Errorf("could not extract owner/repo from URL: %s", remoteURL)
	}

	return "", "", fmt.Errorf("could not parse owner/repo from URL: %s", remoteURL)
}

// containsToken checks if a URL appears to contain a token
func containsToken(remoteURL string) bool {
	// Check for common token patterns in URLs
	tokenPatterns := []string{
		"ghp_",       // GitHub personal access token
		"gho_",       // GitHub OAuth token
		"ghu_",       // GitHub user token
		"ghs_",       // GitHub server token
		"ghr_",       // GitHub refresh token
		"ght_",       // GitHub testing token
		"api_token",  // Common query param name
		"access_token",
		"auth_token",
		"@.*:",       // Basic auth with password (password:token@host)
		"//.*:.*@",   // URL with embedded credentials
	}

	lowerURL := strings.ToLower(remoteURL)
	for _, pattern := range tokenPatterns {
		matched, _ := regexp.MatchString(pattern, lowerURL)
		if matched {
			return true
		}
	}

	return false
}

// isGitHubURL validates that a URL is from GitHub
func isGitHubURL(u *url.URL) bool {
	// Check hostname
	host := strings.ToLower(u.Hostname())
	return host == "github.com" || strings.HasSuffix(host, ".github.com")
}

// Detect attempts to detect the repository from git remote
// Returns cached result if available, otherwise performs detection
func (d *RepoDetector) Detect() (*RepoInfo, error) {
	// Check cache first
	d.mu.RLock()
	if d.cache != nil {
		cached := d.cache
		d.mu.RUnlock()
		if detectorLog != nil {
			detectorLog.Debugf("Using cached repo info: %s/%s", cached.Owner, cached.Repo)
		}
		return &RepoInfo{
			Owner:  cached.Owner,
			Repo:   cached.Repo,
			Source: cached.Source,
			Cached: true,
			RawURL: cached.RawURL,
		}, nil
	}
	d.mu.RUnlock()

	// Perform detection
	wd, err := getWorkingDir()
	if err != nil {
		return nil, err
	}

	repo, err := git.PlainOpen(wd)
	if err != nil {
		return nil, fmt.Errorf("not in a git repository with an origin remote: %w", err)
	}

	// Get the origin remote
	remote, err := repo.Remote("origin")
	if err != nil {
		return nil, fmt.Errorf("not in a git repository with an origin remote: %w", err)
	}

	if len(remote.Config().URLs) == 0 {
		return nil, fmt.Errorf("not in a git repository with an origin remote: no URLs found")
	}

	remoteURL := remote.Config().URLs[0]

	// Parse the URL
	owner, repoName, err := ParseGitURL(remoteURL)
	if err != nil {
		return nil, fmt.Errorf("not in a git repository with an origin remote: %w", err)
	}

	info := &RepoInfo{
		Owner:  owner,
		Repo:   repoName,
		Source: "git_remote",
		Cached: false,
		RawURL: remoteURL,
	}

	// Cache the result
	d.mu.Lock()
	d.cache = info
	d.mu.Unlock()

	if detectorLog != nil {
		detectorLog.Infof("Detected repo from git remote: %s/%s", owner, repoName)
	}

	return info, nil
}

// ClearCache clears the cached repository information
func (d *RepoDetector) ClearCache() {
	d.mu.Lock()
	d.cache = nil
	d.mu.Unlock()
}

// getWorkingDir returns the current working directory or the git root directory
func getWorkingDir() (string, error) {
	return os.Getwd()
}

// FindRemoteByName finds a specific remote by name in the repository
func FindRemoteByName(remoteName string) (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("failed to get working directory: %w", err)
	}

	repo, err := git.PlainOpen(wd)
	if err != nil {
		return "", fmt.Errorf("not in a git repository: %w", err)
	}

	remote, err := repo.Remote(remoteName)
	if err != nil {
		return "", fmt.Errorf("remote '%s' not found: %w", remoteName, err)
	}

	if len(remote.Config().URLs) == 0 {
		return "", fmt.Errorf("remote '%s' has no URLs", remoteName)
	}

	return remote.Config().URLs[0], nil
}

// GetDefaultBranch returns the default branch of the remote repository
func GetDefaultBranch(remoteName string) (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("failed to get working directory: %w", err)
	}

	repo, err := git.PlainOpen(wd)
	if err != nil {
		return "", fmt.Errorf("not in a git repository: %w", err)
	}

	remote, err := repo.Remote(remoteName)
	if err != nil {
		return "", fmt.Errorf("remote '%s' not found: %w", remoteName, err)
	}

	// Get remote config
	cfg := remote.Config()
	if cfg == nil {
		return "", fmt.Errorf("remote '%s' has no config", remoteName)
	}

	// Try to get the fetch refspec to infer default branch
	for _, fetch := range cfg.Fetch {
		refSpec := fetch.String()
		// Common pattern: +refs/heads/main:refs/remotes/origin/main
		// or +refs/heads/master:refs/remotes/origin/master
		if strings.Contains(refSpec, "refs/heads/") {
			parts := strings.Split(refSpec, ":")
			if len(parts) > 0 {
				src := parts[0]
				// Extract branch name from refs/heads/branch
				if strings.HasPrefix(src, "refs/heads/") {
					branch := strings.TrimPrefix(src, "refs/heads/")
					// Handle wildcard refspecs
					if !strings.Contains(branch, "*") {
						return branch, nil
					}
				}
			}
		}
	}

	// Default to "main" if we can't determine
	return "main", nil
}

// DetectRepoInfo is a convenience function that creates a detector and returns info
func DetectRepoInfo() (*RepoInfo, error) {
	detector := NewRepoDetector()
	return detector.Detect()
}

// IsGitRepository checks if the current directory is in a git repository
func IsGitRepository() bool {
	wd, err := os.Getwd()
	if err != nil {
		return false
	}

	_, err = git.PlainOpen(wd)
	return err == nil
}

// HasOriginRemote checks if the repository has an origin remote
func HasOriginRemote() bool {
	wd, err := os.Getwd()
	if err != nil {
		return false
	}

	repo, err := git.PlainOpen(wd)
	if err != nil {
		return false
	}

	_, err = repo.Remote(DefaultRemoteName)
	return err == nil
}

// GetCurrentRemoteURL returns the URL of the origin remote
func GetCurrentRemoteURL() (string, error) {
	return FindRemoteByName(DefaultRemoteName)
}

// ValidateRemoteURL validates a git remote URL
func ValidateRemoteURL(remoteURL string) error {
	// Check if it contains a token
	if containsToken(remoteURL) {
		return fmt.Errorf("URL contains a token (refusing for security)")
	}

	// Check if it's a valid git URL format
	_, _, err := ParseGitURL(remoteURL)
	return err
}

// GetRemoteURL returns the remote URL for the given remote name
func GetRemoteURL(remoteName string) (string, error) {
	return FindRemoteByName(remoteName)
}

// SetRemoteURL sets the remote URL for the given remote name
func SetRemoteURL(remoteName, newURL string) error {
	wd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get working directory: %w", err)
	}

	repo, err := git.PlainOpen(wd)
	if err != nil {
		return fmt.Errorf("not in a git repository: %w", err)
	}

	// Validate the new URL
	if err := ValidateRemoteURL(newURL); err != nil {
		return fmt.Errorf("invalid remote URL: %w", err)
	}

	// Get the remote
	remote, err := repo.Remote(remoteName)
	if err != nil {
		return fmt.Errorf("remote '%s' not found: %w", remoteName, err)
	}

	// Update the remote config
	cfg := remote.Config()
	if len(cfg.URLs) == 0 {
		cfg.URLs = []string{newURL}
	} else {
		cfg.URLs[0] = newURL
	}

	return nil
}

// IsValidGitURL checks if a URL is a valid git URL
func IsValidGitURL(url string) bool {
	_, _, err := ParseGitURL(url)
	return err == nil
}

// IsGitHubURL checks if a URL is from GitHub
func IsGitHubURL(remoteURL string) bool {
	u, err := url.Parse(remoteURL)
	if err != nil {
		// Try parsing as SSH URL
		if strings.Contains(remoteURL, "git@github.com:") {
			return true
		}
		return false
	}
	return isGitHubURL(u)
}

// GetRemoteName returns the default remote name
func GetRemoteName() string {
	return DefaultRemoteName
}

// GetAuthFromURL extracts auth information from a URL (for validation only)
func GetAuthFromURL(remoteURL string) (string, error) {
	if strings.Contains(remoteURL, "git@") {
		return "ssh", nil
	}

	if strings.HasPrefix(remoteURL, "https://") || strings.HasPrefix(remoteURL, "http://") {
		return "https", nil
	}

	if strings.HasPrefix(remoteURL, "git://") {
		return "git", nil
	}

	return "", fmt.Errorf("unknown URL format")
}

// ValidateURLScheme checks if the URL scheme is supported
func ValidateURLScheme(remoteURL string) error {
	auth, err := GetAuthFromURL(remoteURL)
	if err != nil {
		return err
	}

	switch auth {
	case "ssh", "https", "git":
		return nil
	default:
		return fmt.Errorf("unsupported URL scheme: %s", auth)
	}
}

// CloneURLFromString creates a transport.Endpoint from a URL string
func CloneURLFromString(rawURL string) (*transport.Endpoint, error) {
	endpoint, err := transport.NewEndpoint(rawURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse endpoint: %w", err)
	}
	return endpoint, nil
}
