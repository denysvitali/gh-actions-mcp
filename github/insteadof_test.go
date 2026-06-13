package github

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApplyReverseInsteadOf(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		rules    []insteadOfRule
		expected string
	}{
		{
			name: "matching http proxy rule",
			url:  "http://workspace:token@gh-proxy.example/git/owner/repo.git",
			rules: []insteadOfRule{
				{base: "http://workspace:token@gh-proxy.example/git/", value: "https://github.com/"},
			},
			expected: "https://github.com/owner/repo.git",
		},
		{
			name: "matching rule without trailing slash in repo path",
			url:  "http://proxy/git/owner/repo",
			rules: []insteadOfRule{
				{base: "http://proxy/git/", value: "https://github.com/"},
			},
			expected: "https://github.com/owner/repo",
		},
		{
			name: "no matching rule returns input unchanged",
			url:  "https://github.com/owner/repo.git",
			rules: []insteadOfRule{
				{base: "http://other-proxy/git/", value: "https://gitlab.com/"},
			},
			expected: "https://github.com/owner/repo.git",
		},
		{
			name: "empty rules returns input unchanged",
			url:  "https://github.com/owner/repo.git",
			rules: []insteadOfRule{},
			expected: "https://github.com/owner/repo.git",
		},
		{
			name: "first matching rule wins",
			url:  "http://proxy/git/owner/repo.git",
			rules: []insteadOfRule{
				{base: "http://proxy/git/", value: "https://github.com/"},
				{base: "http://proxy/git/", value: "https://gitlab.com/"},
			},
			expected: "https://github.com/owner/repo.git",
		},
		{
			name: "empty base is ignored",
			url:  "https://github.com/owner/repo.git",
			rules: []insteadOfRule{
				{base: "", value: "https://example.com/"},
			},
			expected: "https://github.com/owner/repo.git",
		},
		{
			name: "matching ssh-style insteadOf rule",
			url:  "git@gh-proxy.example:owner/repo.git",
			rules: []insteadOfRule{
				{base: "git@gh-proxy.example:", value: "git@github.com:"},
			},
			expected: "git@github.com:owner/repo.git",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := applyReverseInsteadOf(tt.url, tt.rules)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestParseInsteadOfRules(t *testing.T) {
	input := "url.http://proxy/git/.insteadof https://github.com/\n" +
		"url.git@proxy:.insteadof git@github.com:\n" +
		"url.http://other/.insteadof https://example.com/\n" +
		"\n" +
		"malformed-line\n"

	rules := parseInsteadOfRules(input)
	require.Len(t, rules, 3)

	assert.Equal(t, "http://proxy/git/", rules[0].base)
	assert.Equal(t, "https://github.com/", rules[0].value)

	assert.Equal(t, "git@proxy:", rules[1].base)
	assert.Equal(t, "git@github.com:", rules[1].value)

	assert.Equal(t, "http://other/", rules[2].base)
	assert.Equal(t, "https://example.com/", rules[2].value)
}

func TestReverseInsteadOf_WithTemporaryGitConfig(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "gitconfig")

	configContent := `[url "http://workspace:token@gh-proxy.example/git/"]
	insteadOf = https://github.com/
`
	require.NoError(t, os.WriteFile(configPath, []byte(configContent), 0o600))

	t.Setenv("GIT_CONFIG_GLOBAL", configPath)

	proxyURL := "http://workspace:token@gh-proxy.example/git/owner/repo.git"
	result, err := ReverseInsteadOf(proxyURL)
	require.NoError(t, err)
	assert.Equal(t, "https://github.com/owner/repo.git", result)
}

func TestReverseInsteadOf_NoMatchingRules(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "gitconfig")

	configContent := `[url "http://other-proxy/git/"]
	insteadOf = https://gitlab.com/
`
	require.NoError(t, os.WriteFile(configPath, []byte(configContent), 0o600))

	t.Setenv("GIT_CONFIG_GLOBAL", configPath)

	url := "https://github.com/owner/repo.git"
	result, err := ReverseInsteadOf(url)
	require.NoError(t, err)
	assert.Equal(t, url, result)
}

func TestReverseInsteadOf_NoGitConfig(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "nonexistent")

	t.Setenv("GIT_CONFIG_GLOBAL", configPath)

	url := "https://github.com/owner/repo.git"
	result, err := ReverseInsteadOf(url)
	require.NoError(t, err)
	assert.Equal(t, url, result)
}

func TestInferRepoFromOrigin_GhProxyURL(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "gitconfig")

	configContent := `[url "http://workspace:token@gh-proxy.example/git/"]
	insteadOf = https://github.com/
`
	require.NoError(t, os.WriteFile(configPath, []byte(configContent), 0o600))

	t.Setenv("GIT_CONFIG_GLOBAL", configPath)

	owner, repo, err := InferRepoFromOrigin("http://workspace:token@gh-proxy.example/git/denysvitali/jaeger_flutter.git")
	require.NoError(t, err)
	assert.Equal(t, "denysvitali", owner)
	assert.Equal(t, "jaeger_flutter", repo)
}

func TestParseGitURL_GhProxyURL(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "gitconfig")

	configContent := `[url "http://workspace:token@gh-proxy.example/git/"]
	insteadOf = https://github.com/
`
	require.NoError(t, os.WriteFile(configPath, []byte(configContent), 0o600))

	t.Setenv("GIT_CONFIG_GLOBAL", configPath)

	owner, repo, err := ParseGitURL("http://workspace:token@gh-proxy.example/git/denysvitali/jaeger_flutter.git")
	require.NoError(t, err)
	assert.Equal(t, "denysvitali", owner)
	assert.Equal(t, "jaeger_flutter", repo)
}
