package github

import (
	"fmt"
	"os"
	"sync"

	"github.com/go-git/go-git/v5"
)

const (
	DefaultRemoteName = "origin"
)

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

// resolveRemoteName determines the best remote to use for repo detection.
// It checks the current branch's tracking remote first, then falls back to "origin".
func resolveRemoteName(repo *git.Repository) string {
	head, err := repo.Head()
	if err != nil {
		log.Debugf("Could not get HEAD: %v, falling back to origin", err)
		return DefaultRemoteName
	}

	if !head.Name().IsBranch() {
		log.Debugf("HEAD is not a branch (detached HEAD?), falling back to origin")
		return DefaultRemoteName
	}

	branchName := head.Name().Short()

	cfg, err := repo.Config()
	if err != nil {
		log.Debugf("Could not read repo config: %v, falling back to origin", err)
		return DefaultRemoteName
	}

	branchCfg, ok := cfg.Branches[branchName]
	if !ok || branchCfg.Remote == "" {
		log.Debugf("Branch %q has no tracking remote, falling back to origin", branchName)
		return DefaultRemoteName
	}

	log.Debugf("Branch %q tracks remote %q", branchName, branchCfg.Remote)
	return branchCfg.Remote
}

// Detect attempts to detect the repository from git remote.
// It uses the current branch's tracking remote if available, otherwise falls back to "origin".
// Returns cached result if available, otherwise performs detection.
func (d *RepoDetector) Detect() (*RepoInfo, error) {
	// Check cache first
	d.mu.RLock()
	if d.cache != nil {
		cached := d.cache
		d.mu.RUnlock()
		if log != nil {
			log.Debugf("Using cached repo info: %s/%s", cached.Owner, cached.Repo)
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
		return nil, fmt.Errorf("not in a git repository: %w", err)
	}

	remoteName := resolveRemoteName(repo)

	remote, err := repo.Remote(remoteName)
	if err != nil {
		// If the resolved remote doesn't exist and it's not origin, try origin as fallback
		if remoteName != DefaultRemoteName {
			log.Warnf("Remote %q not found, falling back to origin", remoteName)
			remote, err = repo.Remote(DefaultRemoteName)
			if err != nil {
				return nil, fmt.Errorf("neither remote %q nor origin found: %w", remoteName, err)
			}
			remoteName = DefaultRemoteName
		} else {
			return nil, fmt.Errorf("remote %q not found: %w", remoteName, err)
		}
	}

	if len(remote.Config().URLs) == 0 {
		return nil, fmt.Errorf("remote %q has no URLs", remoteName)
	}

	remoteURL := remote.Config().URLs[0]

	// Parse the URL
	owner, repoName, err := ParseGitURL(remoteURL)
	if err != nil {
		return nil, fmt.Errorf("could not parse owner/repo from remote %q: %w", remoteName, err)
	}

	source := fmt.Sprintf("git_remote(%s)", remoteName)
	info := &RepoInfo{
		Owner:  owner,
		Repo:   repoName,
		Source: source,
		Cached: false,
		// Remotes rewritten through a proxy can embed credentials; RawURL is
		// surfaced to MCP clients, so never expose the password.
		RawURL: RedactURL(remoteURL),
	}

	// Cache the result
	d.mu.Lock()
	d.cache = info
	d.mu.Unlock()

	if log != nil {
		log.Infof("Detected repo from remote %q: %s/%s", remoteName, owner, repoName)
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

// DetectRepoInfo is a convenience function that creates a detector and returns info
func DetectRepoInfo() (*RepoInfo, error) {
	detector := NewRepoDetector()
	return detector.Detect()
}
