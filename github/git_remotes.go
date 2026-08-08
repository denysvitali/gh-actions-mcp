package github

import (
	"fmt"
	"os"
	"strings"

	"github.com/go-git/go-git/v5"
)

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
func GetDefaultBranch(remoteName string) (string, error) { //nolint:gocognit,nestif // Refspec parsing and fallbacks are kept in one small operation.
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
		if strings.Contains(refSpec, "refs/heads/") { //nolint:nestif // Refspec parsing is deliberately defensive.
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

	// Persist the updated config back to the repository
	repoConfig, err := repo.Config()
	if err != nil {
		return fmt.Errorf("failed to get repo config: %w", err)
	}
	repoConfig.Remotes[remoteName] = cfg
	if err := repo.SetConfig(repoConfig); err != nil {
		return fmt.Errorf("failed to save remote config: %w", err)
	}

	return nil
}

// GetRemoteName returns the default remote name
func GetRemoteName() string {
	return DefaultRemoteName
}
