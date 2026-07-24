package github

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeGitConfig writes content to a temporary file and points git's global
// config at it, with the system config disabled so the host machine's own
// gh-proxy setup cannot leak into the test.
func writeGitConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gitconfig")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	t.Setenv("GIT_CONFIG_GLOBAL", path)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	return path
}

func TestParseGitConfigNull(t *testing.T) {
	// "key\nvalue" records separated by NUL, plus a valueless boolean.
	output := "url.http://proxy/git/.insteadof\nhttps://github.com/\x00" +
		"user.name\nAda Lovelace\x00" +
		"core.bare\x00" +
		"url.http://proxy/git/.pushinsteadof\nssh://git@github.com/\x00"

	entries := parseGitConfigNull(output)
	require.Len(t, entries, 4)

	assert.Equal(t, gitConfigEntry{key: "url.http://proxy/git/.insteadof", value: "https://github.com/"}, entries[0])
	// Values with spaces survive because records are NUL-delimited.
	assert.Equal(t, gitConfigEntry{key: "user.name", value: "Ada Lovelace"}, entries[1])
	assert.Equal(t, gitConfigEntry{key: "core.bare", value: ""}, entries[2])
	assert.Equal(t, "url.http://proxy/git/.pushinsteadof", entries[3].key)
}

func TestParseGitConfigNull_EmptyInput(t *testing.T) {
	assert.Empty(t, parseGitConfigNull(""))
	assert.Empty(t, parseGitConfigNull("\x00\x00"))
}

func TestEntriesToInsteadOfRules(t *testing.T) {
	entries := []gitConfigEntry{
		{key: "user.email", value: "a@b.c"},
		{key: "url.http://proxy/git/.insteadof", value: "https://github.com/"},
		{key: "url.ssh://git@proxy/.pushInsteadOf", value: "git@github.com:"},
		// A subsection containing dots and an "insteadof"-like substring.
		{key: "url.http://a.b.c/git/.insteadof", value: "https://github.com/"},
		// Ignored: not a url.* rewrite rule.
		{key: "urlx.foo.insteadof", value: "https://github.com/"},
		{key: "url.http://proxy/.other", value: "x"},
		// Ignored: empty value or empty base.
		{key: "url.http://empty/.insteadof", value: ""},
		{key: "url..insteadof", value: "https://github.com/"},
	}

	rules := entriesToInsteadOfRules(entries)
	require.Len(t, rules, 3)

	assert.Equal(t, insteadOfRule{base: "http://proxy/git/", value: "https://github.com/"}, rules[0])
	assert.Equal(t, insteadOfRule{base: "ssh://git@proxy/", value: "git@github.com:", push: true}, rules[1])
	assert.Equal(t, "http://a.b.c/git/", rules[2].base)
}

func TestProxyFromRewriteTarget(t *testing.T) {
	tests := []struct {
		name       string
		target     string
		wantAPI    string
		wantUser   string
		wantPass   string
		wantErrStr string
	}{
		{
			name:     "gh-proxy git mount becomes api mount",
			target:   "http://user:secret@gh-proxy.example/git/",
			wantAPI:  "http://gh-proxy.example/api/",
			wantUser: "user",
			wantPass: "secret",
		},
		{
			name:    "no credentials",
			target:  "https://gh-proxy.example/git/",
			wantAPI: "https://gh-proxy.example/api/",
		},
		{
			name:    "missing trailing slash",
			target:  "https://gh-proxy.example/git",
			wantAPI: "https://gh-proxy.example/api/",
		},
		{
			name:    "nested mount path is preserved",
			target:  "https://proxy.example/github/git/",
			wantAPI: "https://proxy.example/github/api/",
		},
		{
			name:    "root path gets api appended",
			target:  "https://proxy.example/",
			wantAPI: "https://proxy.example/api/",
		},
		{
			name:    "explicit port preserved",
			target:  "http://gh-proxy.example:8080/git/",
			wantAPI: "http://gh-proxy.example:8080/api/",
		},
		{
			name:     "username only",
			target:   "http://user@gh-proxy.example/git/",
			wantAPI:  "http://gh-proxy.example/api/",
			wantUser: "user",
		},
		{
			name:       "ssh scheme rejected",
			target:     "ssh://git@gh-proxy.example/",
			wantErrStr: "unsupported rewrite target scheme",
		},
		{
			name:       "scp-like target rejected",
			target:     "git@gh-proxy.example:",
			wantErrStr: "parse rewrite target",
		},
		{
			name:       "github itself is not a proxy",
			target:     "https://github.com/",
			wantErrStr: "GitHub itself",
		},
		{
			name:       "no host",
			target:     "http:///git/",
			wantErrStr: "no host",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, err := proxyFromRewriteTarget(tt.target)
			if tt.wantErrStr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErrStr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantAPI, info.APIBaseURL)
			assert.Equal(t, tt.wantUser, info.Username)
			assert.Equal(t, tt.wantPass, info.Password)
			// The API base URL must never carry credentials.
			assert.NotContains(t, info.APIBaseURL, "@")
		})
	}
}

func TestDetectProxyFromRules(t *testing.T) {
	tests := []struct {
		name     string
		rules    []insteadOfRule
		wantNil  bool
		wantAPI  string
		wantUser string
		wantPass string
		wantPush bool
	}{
		{
			name:    "no rules",
			wantNil: true,
		},
		{
			name: "rule for another forge is ignored",
			rules: []insteadOfRule{
				{base: "http://proxy/git/", value: "https://gitlab.com/"},
			},
			wantNil: true,
		},
		{
			name: "ssh rewrite target is not usable for the REST API",
			rules: []insteadOfRule{
				{base: "ssh://git@proxy/", value: "https://github.com/"},
			},
			wantNil: true,
		},
		{
			name: "https github rewrite with credentials",
			rules: []insteadOfRule{
				{base: "http://user:secret@gh-proxy.example/git/", value: "https://github.com/"},
			},
			wantAPI:  "http://gh-proxy.example/api/",
			wantUser: "user",
			wantPass: "secret",
		},
		{
			name: "ssh insteadOf value still identifies github",
			rules: []insteadOfRule{
				{base: "https://gh-proxy.example/git/", value: "git@github.com:"},
			},
			wantAPI: "https://gh-proxy.example/api/",
		},
		{
			name: "credentialed rule beats anonymous rule",
			rules: []insteadOfRule{
				{base: "http://anon.example/git/", value: "http://github.com/"},
				{base: "http://user:secret@creds.example/git/", value: "https://github.com/"},
			},
			wantAPI:  "http://creds.example/api/",
			wantUser: "user",
			wantPass: "secret",
		},
		{
			name: "fetch rule beats push rule even when push has credentials",
			rules: []insteadOfRule{
				{base: "http://user:secret@push.example/git/", value: "https://github.com/", push: true},
				{base: "http://fetch.example/git/", value: "https://github.com/"},
			},
			wantAPI: "http://fetch.example/api/",
		},
		{
			name: "push-only rule is used as a last resort",
			rules: []insteadOfRule{
				{base: "http://user:secret@push.example/git/", value: "https://github.com/", push: true},
			},
			wantAPI:  "http://push.example/api/",
			wantUser: "user",
			wantPass: "secret",
			wantPush: true,
		},
		{
			name: "github enterprise subdomain rewrite is detected",
			rules: []insteadOfRule{
				{base: "https://proxy.example/git/", value: "https://api.github.com/"},
			},
			wantAPI: "https://proxy.example/api/",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := detectProxyFromRules(tt.rules)
			if tt.wantNil {
				assert.Nil(t, info)
				return
			}
			require.NotNil(t, info)
			assert.Equal(t, tt.wantAPI, info.APIBaseURL)
			assert.Equal(t, tt.wantUser, info.Username)
			assert.Equal(t, tt.wantPass, info.Password)
			assert.Equal(t, tt.wantPush, info.Push)
		})
	}
}

func TestDetectProxy_FromGitConfig(t *testing.T) {
	writeGitConfig(t, `[url "http://workspace:s3cr3t@gh-proxy.example/git/"]
	insteadOf = https://github.com/
`)

	info, err := DetectProxy(t.TempDir())
	require.NoError(t, err)
	require.NotNil(t, info)

	assert.Equal(t, "http://gh-proxy.example/api/", info.APIBaseURL)
	assert.Equal(t, "workspace", info.Username)
	assert.Equal(t, "s3cr3t", info.Password)
	assert.Equal(t, "https://github.com/", info.MatchedPrefix)
	assert.True(t, info.HasCredentials())
}

func TestDetectProxy_MultipleRulesPicksHTTPWithCredentials(t *testing.T) {
	// Mirrors a realistic setup: an anonymous rule for http://github.com and a
	// credentialed one for https://github.com.
	writeGitConfig(t, `[url "http://gh-proxy.example/git/"]
	insteadOf = http://github.com/
[url "http://workspace:s3cr3t@gh-proxy.example/git/"]
	insteadOf = https://github.com/
`)

	info, err := DetectProxy(t.TempDir())
	require.NoError(t, err)
	require.NotNil(t, info)
	assert.Equal(t, "workspace", info.Username)
	assert.Equal(t, "s3cr3t", info.Password)
}

func TestDetectProxy_NoRewriteRules(t *testing.T) {
	writeGitConfig(t, "[user]\n\tname = Nobody\n")

	info, err := DetectProxy(t.TempDir())
	require.NoError(t, err)
	assert.Nil(t, info)
}

func TestDetectProxy_IgnoresNonGitHubRewrites(t *testing.T) {
	writeGitConfig(t, `[url "http://mirror.example/git/"]
	insteadOf = https://gitlab.com/
`)

	info, err := DetectProxy(t.TempDir())
	require.NoError(t, err)
	assert.Nil(t, info)
}

func TestDetectProxy_FollowsIncludes(t *testing.T) {
	dir := t.TempDir()
	included := filepath.Join(dir, "proxy.gitconfig")
	require.NoError(t, os.WriteFile(included, []byte(`[url "http://user:pw@included-proxy.example/git/"]
	insteadOf = https://github.com/
`), 0o600))

	writeGitConfig(t, "[include]\n\tpath = "+included+"\n")

	info, err := DetectProxy(t.TempDir())
	require.NoError(t, err)
	require.NotNil(t, info)
	assert.Equal(t, "http://included-proxy.example/api/", info.APIBaseURL)
	assert.Equal(t, "user", info.Username)
}

func TestDetectProxy_LocalRepoConfigWins(t *testing.T) {
	writeGitConfig(t, `[url "http://global-proxy.example/git/"]
	insteadOf = https://github.com/
`)

	repoDir := t.TempDir()
	runGit(t, repoDir, "init")
	runGit(t, repoDir, "config", "url.http://user:pw@local-proxy.example/git/.insteadOf", "https://github.com/")

	info, err := DetectProxy(repoDir)
	require.NoError(t, err)
	require.NotNil(t, info)
	// The local rule carries credentials, so it outranks the global one.
	assert.Equal(t, "http://local-proxy.example/api/", info.APIBaseURL)
}

func TestProxyInfo_StringNeverLeaksPassword(t *testing.T) {
	info := &ProxyInfo{
		APIBaseURL:    "http://gh-proxy.example/api/",
		Username:      "workspace",
		Password:      "super-secret-value",
		MatchedPrefix: "https://github.com/",
		RewriteTarget: RedactURL("http://workspace:super-secret-value@gh-proxy.example/git/"),
	}

	s := info.String()
	assert.NotContains(t, s, "super-secret-value")
	assert.Contains(t, s, "http://gh-proxy.example/api/")
	assert.Contains(t, s, "basic(workspace)")

	assert.Equal(t, "<no proxy>", (*ProxyInfo)(nil).String())
	assert.False(t, (*ProxyInfo)(nil).HasCredentials())
	assert.False(t, (&ProxyInfo{Username: "u"}).HasCredentials())
}

func TestRedactURL(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		expected string
	}{
		{
			name:     "password redacted, username kept",
			in:       "http://workspace:s3cr3t@gh-proxy.example/git/owner/repo.git",
			expected: "http://workspace:***@gh-proxy.example/git/owner/repo.git",
		},
		{
			name:     "username only is left alone",
			in:       "http://workspace@gh-proxy.example/git/",
			expected: "http://workspace@gh-proxy.example/git/",
		},
		{
			name:     "no credentials",
			in:       "https://github.com/owner/repo.git",
			expected: "https://github.com/owner/repo.git",
		},
		{
			name:     "scp-like ssh URL untouched",
			in:       "git@github.com:owner/repo.git",
			expected: "git@github.com:owner/repo.git",
		},
		{
			name:     "empty string",
			in:       "",
			expected: "",
		},
		{
			name:     "token as username with empty password",
			in:       "https://ghp_deadbeef:@github.com/owner/repo.git",
			expected: "https://ghp_deadbeef:***@github.com/owner/repo.git",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, RedactURL(tt.in))
		})
	}
}

func TestIsGitHubPrefix(t *testing.T) {
	yes := []string{
		"https://github.com/",
		"http://github.com/",
		"git://github.com/",
		"ssh://git@github.com/",
		"git@github.com:",
		"https://api.github.com/",
		"https://GitHub.com/denysvitali/",
	}
	for _, v := range yes {
		assert.True(t, isGitHubPrefix(v), "expected %q to be a GitHub prefix", v)
	}

	no := []string{
		"",
		"   ",
		"https://gitlab.com/",
		"https://github.com.evil.example/",
		"git@gitlab.com:",
		"github.com/denysvitali",
		"http://gh-proxy.example/git/",
	}
	for _, v := range no {
		assert.False(t, isGitHubPrefix(v), "expected %q not to be a GitHub prefix", v)
	}
}

func TestIsGitHubHost(t *testing.T) {
	assert.True(t, isGitHubHost("github.com"))
	assert.True(t, isGitHubHost("GITHUB.COM"))
	assert.True(t, isGitHubHost("api.github.com"))
	assert.True(t, isGitHubHost("github.com."))
	assert.False(t, isGitHubHost("github.com.evil.example"))
	assert.False(t, isGitHubHost("notgithub.com"))
	assert.False(t, isGitHubHost(""))
}

func TestLoadGitConfigEntries_MissingConfigIsNotAnError(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "does-not-exist"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	entries, err := loadGitConfigEntries(t.TempDir())
	require.NoError(t, err)
	assert.Empty(t, entries)
}
