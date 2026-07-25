package github

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/transport"
)

// ParseGitURL parses a git URL and extracts owner/repo
// Supports SSH, HTTPS, git://, and bare formats. It also reverses git
// url.<base>.insteadOf rewrites (e.g. gh-proxy) before parsing.
func ParseGitURL(remoteURL string) (string, string, error) {
	// Undo git insteadOf rewrites such as gh-proxy so that a proxied remote
	// URL is translated back to its original github.com form.
	if reversed, revErr := ReverseInsteadOf(remoteURL); revErr == nil {
		remoteURL = reversed
	}

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
		"ghp_",      // GitHub personal access token
		"gho_",      // GitHub OAuth token
		"ghu_",      // GitHub user token
		"ghs_",      // GitHub server token
		"ghr_",      // GitHub refresh token
		"ght_",      // GitHub testing token
		"api_token", // Common query param name
		"access_token",
		"auth_token",
		"//.*:.*@", // URL with embedded credentials (e.g. https://user:token@host)
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
