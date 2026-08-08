package config

import (
	"fmt"
	"runtime"
	"strings"
)

// Validate guarantees that, when it returns nil, the Config has a usable token
// (see [Config.ValidateToken]) and a default repository owner and name. It may
// mutate c.Token as a side effect of token discovery. The error text names the
// env var, config key and flag the user can set, so it is safe to surface
// verbatim.
func (c *Config) Validate() error {
	if err := c.ValidateToken(); err != nil {
		return err
	}
	if c.RepoOwner == "" {
		return fmt.Errorf("repository owner is required. Set GH_REPO_OWNER env var, 'repo_owner' in config, or use --repo-owner flag")
	}
	if c.RepoName == "" {
		return fmt.Errorf("repository name is required. Set GH_REPO_NAME env var, 'repo_name' in config, or use --repo-name flag")
	}
	return nil
}

// ValidateToken guarantees that, when it returns nil, c.Token is non-empty.
// If it was empty on entry, the token is discovered from the macOS keychain
// (macOS only) and then from `gh auth token`, in that order, and c.Token is
// updated. It shells out, so it is not cheap and must not be called on a hot
// path. Nothing is validated about the token's contents — only that one exists.
func (c *Config) ValidateToken() error {
	if c.Token == "" {
		c.tokenFromKeychain()
	}
	if c.Token == "" {
		c.tokenFromGHCLI()
	}

	if c.Token == "" {
		return fmt.Errorf("GitHub token is required. Set GITHUB_TOKEN, set 'token' in config, or run 'gh auth login'")
	}
	return nil
}

// tokenFromKeychain fills c.Token from the macOS keychain. It is a no-op on
// every other platform, and leaves c.Token untouched on failure.
func (c *Config) tokenFromKeychain() {
	if runtime.GOOS != "darwin" {
		return
	}
	token, err := keychainTokenProvider()
	if err != nil {
		log.Debugf("Could not get token from keychain: %v", err)
		return
	}
	c.Token = token
	log.Infof("Obtained GitHub token from macOS keychain")
}

// tokenFromGHCLI fills c.Token from `gh auth token`. It leaves c.Token
// untouched when the CLI is missing, not logged in, or prints nothing.
func (c *Config) tokenFromGHCLI() {
	token, err := ghTokenProvider()
	if err != nil || token == "" {
		return
	}
	c.Token = token
	log.Infof("Obtained GitHub token from gh CLI authentication")
}

// IsAuthenticationError reports whether err looks like an authentication or
// authorisation failure. It guarantees only a heuristic: the check is a
// substring match on the lower-cased message, so it can produce false
// positives on unrelated errors that happen to mention 401/403 or "forbidden".
// Use it to improve a message, never to make a security decision.
func IsAuthenticationError(err error) bool {
	if err == nil {
		return false
	}
	errStr := strings.ToLower(err.Error())
	return strings.Contains(errStr, "401") ||
		strings.Contains(errStr, "403") ||
		strings.Contains(errStr, "bad credentials") ||
		strings.Contains(errStr, "authentication") ||
		strings.Contains(errStr, "resource not accessible by personal access token") ||
		strings.Contains(errStr, "insufficient permission") ||
		strings.Contains(errStr, "forbidden")
}
