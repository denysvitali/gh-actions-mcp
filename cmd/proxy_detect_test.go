package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/denysvitali/gh-actions-mcp/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withIsolatedGitConfig points git's global config at a temp file and disables
// the system config so the host's own gh-proxy rewrite cannot leak in.
func withIsolatedGitConfig(t *testing.T, content string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gitconfig")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	t.Setenv("GIT_CONFIG_GLOBAL", path)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Chdir(t.TempDir())
}

func TestApplyGitProxyDetection_DetectsProxyFromGitConfig(t *testing.T) {
	withIsolatedGitConfig(t, `[url "http://workspace:s3cr3t@gh-proxy.example/git/"]
	insteadOf = https://github.com/
`)

	cfg := &config.Config{GitProxyDetect: true}
	applyGitProxyDetection(cfg)

	assert.Equal(t, "http://gh-proxy.example/api/", cfg.APIBaseURL)
	assert.Equal(t, "workspace", cfg.AuthUsername)
	assert.Equal(t, "s3cr3t", cfg.Token)
}

func TestApplyGitProxyDetection_ExplicitAPIBaseURLWins(t *testing.T) {
	withIsolatedGitConfig(t, `[url "http://workspace:s3cr3t@gh-proxy.example/git/"]
	insteadOf = https://github.com/
`)

	cfg := &config.Config{
		APIBaseURL:     "http://configured.example/api/",
		GitProxyDetect: true,
	}
	applyGitProxyDetection(cfg)

	assert.Equal(t, "http://configured.example/api/", cfg.APIBaseURL)
	assert.Empty(t, cfg.AuthUsername)
	assert.Empty(t, cfg.Token)
}

func TestApplyGitProxyDetection_KeepsConfiguredCredentials(t *testing.T) {
	withIsolatedGitConfig(t, `[url "http://workspace:s3cr3t@gh-proxy.example/git/"]
	insteadOf = https://github.com/
`)

	cfg := &config.Config{
		GitProxyDetect: true,
		AuthUsername:   "configured-user",
		Token:          "configured-token",
	}
	applyGitProxyDetection(cfg)

	assert.Equal(t, "http://gh-proxy.example/api/", cfg.APIBaseURL)
	assert.Equal(t, "configured-user", cfg.AuthUsername)
	assert.Equal(t, "configured-token", cfg.Token)
}

func TestApplyGitProxyDetection_ProxyCredentialsOverrideGitHubToken(t *testing.T) {
	// A real GitHub PAT is useless against a proxy that authenticates its own
	// consumers: the git-config credentials must win when no auth_username was
	// explicitly configured.
	withIsolatedGitConfig(t, `[url "http://workspace:s3cr3t@gh-proxy.example/git/"]
	insteadOf = https://github.com/
`)

	cfg := &config.Config{GitProxyDetect: true, Token: "ghp_githubpat"}
	applyGitProxyDetection(cfg)

	assert.Equal(t, "workspace", cfg.AuthUsername)
	assert.Equal(t, "s3cr3t", cfg.Token)
}

func TestApplyGitProxyDetection_NoRulesLeavesConfigUntouched(t *testing.T) {
	withIsolatedGitConfig(t, "[user]\n\tname = Nobody\n")

	cfg := &config.Config{GitProxyDetect: true, Token: "keep-me"}
	applyGitProxyDetection(cfg)

	assert.Empty(t, cfg.APIBaseURL)
	assert.Empty(t, cfg.AuthUsername)
	assert.Equal(t, "keep-me", cfg.Token)
}

func TestApplyGitProxyDetection_Disabled(t *testing.T) {
	withIsolatedGitConfig(t, `[url "http://workspace:s3cr3t@gh-proxy.example/git/"]
	insteadOf = https://github.com/
`)

	cfg := &config.Config{GitProxyDetect: false}
	applyGitProxyDetection(cfg)

	assert.Empty(t, cfg.APIBaseURL)
	assert.Empty(t, cfg.AuthUsername)
}

func TestApplyGitProxyDetection_ProxyWithoutCredentialsKeepsToken(t *testing.T) {
	withIsolatedGitConfig(t, `[url "http://gh-proxy.example/git/"]
	insteadOf = https://github.com/
`)

	cfg := &config.Config{GitProxyDetect: true, Token: "ghp_githubpat"}
	applyGitProxyDetection(cfg)

	assert.Equal(t, "http://gh-proxy.example/api/", cfg.APIBaseURL)
	assert.Empty(t, cfg.AuthUsername)
	assert.Equal(t, "ghp_githubpat", cfg.Token)
}

func TestApplyGitProxyDetection_ThenSplitProxyCredentials(t *testing.T) {
	// Ordering case from loadConfigWithOptions: a dotted proxy token is set
	// but no api_base_url exists yet, so the split can only happen after
	// detection has derived the base URL.
	withIsolatedGitConfig(t, `[url "http://gh-proxy.example/git/"]
	insteadOf = https://github.com/
`)

	cfg := &config.Config{GitProxyDetect: true, Token: "workspace.s3cr3t"}

	cfg.SplitProxyCredentials()
	assert.Empty(t, cfg.AuthUsername, "split must not happen before a base URL exists")

	applyGitProxyDetection(cfg)
	cfg.SplitProxyCredentials()

	assert.Equal(t, "http://gh-proxy.example/api/", cfg.APIBaseURL)
	assert.Equal(t, "workspace", cfg.AuthUsername)
	assert.Equal(t, "s3cr3t", cfg.Token)
}
