package github

import (
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runGit runs a git command in dir and fails the test on error.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "git %v failed: %s", args, out)
	return string(out)
}

// newTestRepo creates a real git repository with an origin remote and makes it
// the working directory for the duration of the test.
func newTestRepo(t *testing.T, originURL string) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "--initial-branch=main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	if originURL != "" {
		runGit(t, dir, "remote", "add", "origin", originURL)
	}
	t.Chdir(dir)
	return dir
}

func TestDetect_HTTPSOrigin(t *testing.T) {
	writeGitConfig(t, "")
	newTestRepo(t, "https://github.com/denysvitali/gh-actions-mcp.git")

	info, err := NewRepoDetector().Detect()
	require.NoError(t, err)
	assert.Equal(t, "denysvitali", info.Owner)
	assert.Equal(t, "gh-actions-mcp", info.Repo)
	assert.Equal(t, "git_remote(origin)", info.Source)
	assert.False(t, info.Cached)
}

func TestDetect_SSHOrigin(t *testing.T) {
	writeGitConfig(t, "")
	newTestRepo(t, "git@github.com:denysvitali/gh-actions-mcp.git")

	info, err := NewRepoDetector().Detect()
	require.NoError(t, err)
	assert.Equal(t, "denysvitali", info.Owner)
	assert.Equal(t, "gh-actions-mcp", info.Repo)
}

func TestDetect_ProxyRewrittenOriginResolvesToGitHubRepo(t *testing.T) {
	// A repo cloned through gh-proxy stores the rewritten URL, credentials and
	// all. Detection must undo the rewrite and must not leak the password.
	writeGitConfig(t, `[url "http://workspace:s3cr3t@gh-proxy.example/git/"]
	insteadOf = https://github.com/
`)
	newTestRepo(t, "http://workspace:s3cr3t@gh-proxy.example/git/denysvitali/gh-actions-mcp.git")

	info, err := NewRepoDetector().Detect()
	require.NoError(t, err)
	assert.Equal(t, "denysvitali", info.Owner)
	assert.Equal(t, "gh-actions-mcp", info.Repo)
	assert.NotContains(t, info.RawURL, "s3cr3t")
	assert.Equal(t, "http://workspace:***@gh-proxy.example/git/denysvitali/gh-actions-mcp.git", info.RawURL)
}

func TestDetect_UsesTrackingRemoteOfCurrentBranch(t *testing.T) {
	writeGitConfig(t, "")
	dir := newTestRepo(t, "https://github.com/denysvitali/from-origin.git")
	runGit(t, dir, "remote", "add", "upstream", "https://github.com/upstream-owner/from-upstream.git")
	runGit(t, dir, "commit", "--allow-empty", "-m", "init")
	runGit(t, dir, "config", "branch.main.remote", "upstream")
	runGit(t, dir, "config", "branch.main.merge", "refs/heads/main")

	info, err := NewRepoDetector().Detect()
	require.NoError(t, err)
	assert.Equal(t, "upstream-owner", info.Owner)
	assert.Equal(t, "from-upstream", info.Repo)
	assert.Equal(t, "git_remote(upstream)", info.Source)
}

func TestDetect_FallsBackToOriginWhenTrackingRemoteMissing(t *testing.T) {
	writeGitConfig(t, "")
	dir := newTestRepo(t, "https://github.com/denysvitali/from-origin.git")
	runGit(t, dir, "commit", "--allow-empty", "-m", "init")
	// Point the branch at a remote that does not exist.
	runGit(t, dir, "config", "branch.main.remote", "ghost")

	info, err := NewRepoDetector().Detect()
	require.NoError(t, err)
	assert.Equal(t, "denysvitali", info.Owner)
	assert.Equal(t, "from-origin", info.Repo)
	assert.Equal(t, "git_remote(origin)", info.Source)
}

func TestDetect_NoRemoteIsAnError(t *testing.T) {
	writeGitConfig(t, "")
	newTestRepo(t, "")

	_, err := NewRepoDetector().Detect()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "origin")
}

func TestDetect_NonGitHubRemoteIsAnError(t *testing.T) {
	writeGitConfig(t, "")
	newTestRepo(t, "https://gitlab.com/denysvitali/something.git")

	_, err := NewRepoDetector().Detect()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a GitHub URL")
}

func TestDetect_NotAGitRepository(t *testing.T) {
	writeGitConfig(t, "")
	t.Chdir(t.TempDir())

	_, err := NewRepoDetector().Detect()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not in a git repository")
}

func TestDetect_CachesAndClears(t *testing.T) {
	writeGitConfig(t, "")
	dir := newTestRepo(t, "https://github.com/denysvitali/first.git")

	d := NewRepoDetector()
	first, err := d.Detect()
	require.NoError(t, err)
	assert.False(t, first.Cached)

	// Change the remote: the cached answer must still be returned.
	runGit(t, dir, "remote", "set-url", "origin", "https://github.com/denysvitali/second.git")

	cached, err := d.Detect()
	require.NoError(t, err)
	assert.True(t, cached.Cached)
	assert.Equal(t, "first", cached.Repo)

	d.ClearCache()
	fresh, err := d.Detect()
	require.NoError(t, err)
	assert.False(t, fresh.Cached)
	assert.Equal(t, "second", fresh.Repo)
}

func TestIsGitRepositoryAndHasOriginRemote(t *testing.T) {
	writeGitConfig(t, "")

	t.Run("outside a repo", func(t *testing.T) {
		t.Chdir(t.TempDir())
		assert.False(t, IsGitRepository())
		assert.False(t, HasOriginRemote())
	})

	t.Run("inside a repo with origin", func(t *testing.T) {
		newTestRepo(t, "https://github.com/denysvitali/gh-actions-mcp.git")
		assert.True(t, IsGitRepository())
		assert.True(t, HasOriginRemote())

		url, err := GetCurrentRemoteURL()
		require.NoError(t, err)
		assert.Equal(t, "https://github.com/denysvitali/gh-actions-mcp.git", url)
	})

	t.Run("inside a repo without origin", func(t *testing.T) {
		newTestRepo(t, "")
		assert.True(t, IsGitRepository())
		assert.False(t, HasOriginRemote())

		_, err := GetCurrentRemoteURL()
		require.Error(t, err)
	})
}

func TestFindRemoteByName(t *testing.T) {
	writeGitConfig(t, "")
	dir := newTestRepo(t, "https://github.com/denysvitali/gh-actions-mcp.git")
	runGit(t, dir, "remote", "add", "fork", "git@github.com:someone/fork.git")

	url, err := FindRemoteByName("fork")
	require.NoError(t, err)
	assert.Equal(t, "git@github.com:someone/fork.git", url)

	_, err = FindRemoteByName("nope")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestSetRemoteURL(t *testing.T) {
	writeGitConfig(t, "")
	newTestRepo(t, "https://github.com/denysvitali/old.git")

	require.NoError(t, SetRemoteURL("origin", "https://github.com/denysvitali/new.git"))

	url, err := GetRemoteURL("origin")
	require.NoError(t, err)
	assert.Equal(t, "https://github.com/denysvitali/new.git", url)

	// Invalid URLs are rejected and leave the remote untouched.
	require.Error(t, SetRemoteURL("origin", "https://gitlab.com/denysvitali/new.git"))
	url, err = GetRemoteURL("origin")
	require.NoError(t, err)
	assert.Equal(t, "https://github.com/denysvitali/new.git", url)
}

func TestGetDefaultBranch(t *testing.T) {
	writeGitConfig(t, "")
	newTestRepo(t, "https://github.com/denysvitali/gh-actions-mcp.git")

	branch, err := GetDefaultBranch("origin")
	require.NoError(t, err)
	// The default refspec is a wildcard, so detection falls back to main.
	assert.Equal(t, "main", branch)

	_, err = GetDefaultBranch("missing")
	require.Error(t, err)
}

func TestValidateRemoteURL(t *testing.T) {
	writeGitConfig(t, "")

	require.NoError(t, ValidateRemoteURL("https://github.com/owner/repo.git"))
	require.NoError(t, ValidateRemoteURL("git@github.com:owner/repo.git"))
	require.Error(t, ValidateRemoteURL("https://x:ghp_deadbeefcafe@github.com/owner/repo.git"))
	require.Error(t, ValidateRemoteURL("ftp://github.com/owner/repo.git"))
}

func TestGetAuthFromURLAndScheme(t *testing.T) {
	tests := []struct {
		url      string
		expected string
		wantErr  bool
	}{
		{url: "git@github.com:owner/repo.git", expected: "ssh"},
		{url: "https://github.com/owner/repo.git", expected: "https"},
		{url: "http://github.com/owner/repo.git", expected: "https"},
		{url: "git://github.com/owner/repo.git", expected: "git"},
		{url: "ftp://github.com/owner/repo.git", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			auth, err := GetAuthFromURL(tt.url)
			if tt.wantErr {
				require.Error(t, err)
				require.Error(t, ValidateURLScheme(tt.url))
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.expected, auth)
			require.NoError(t, ValidateURLScheme(tt.url))
		})
	}
}
