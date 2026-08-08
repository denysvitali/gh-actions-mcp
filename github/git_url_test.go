package github

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseGitURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		url       string
		wantOwner string
		wantRepo  string
		wantErr   string
	}{
		// bare owner/repo
		{name: "bare owner/repo", url: "owner/repo", wantOwner: "owner", wantRepo: "repo"},
		{name: "bare owner/repo.git", url: "owner/repo.git", wantOwner: "owner", wantRepo: "repo"},
		{name: "bare single segment", url: "repo", wantErr: "could not parse owner/repo from URL"},
		{name: "bare three segments", url: "a/b/c", wantErr: "could not parse owner/repo from URL"},

		// SSH
		{name: "ssh", url: "git@github.com:owner/repo.git", wantOwner: "owner", wantRepo: "repo"},
		{name: "ssh without .git", url: "git@github.com:owner/repo", wantOwner: "owner", wantRepo: "repo"},
		{name: "ssh with extra slash", url: "git@github.com:/owner/repo.git", wantOwner: "owner", wantRepo: "repo"},
		// The SSH branch does NOT validate the host, so enterprise hosts pass.
		{name: "ssh enterprise host is accepted", url: "git@github.example.com:owner/repo.git", wantOwner: "owner", wantRepo: "repo"},
		{name: "ssh scheme form falls through to error", url: "ssh://git@github.com/owner/repo.git", wantErr: "could not parse owner/repo from URL"},

		// HTTPS / HTTP
		{name: "https", url: "https://github.com/owner/repo.git", wantOwner: "owner", wantRepo: "repo"},
		{name: "https without .git", url: "https://github.com/owner/repo", wantOwner: "owner", wantRepo: "repo"},
		{name: "http", url: "http://github.com/owner/repo.git", wantOwner: "owner", wantRepo: "repo"},
		{name: "https subdomain of github.com", url: "https://www.github.com/owner/repo", wantOwner: "owner", wantRepo: "repo"},
		// Extra path segments are tolerated: only the first two are used.
		{name: "https with extra path segments", url: "https://github.com/owner/repo/tree/main", wantOwner: "owner", wantRepo: "repo"},
		{name: "https non-GitHub host is rejected", url: "https://example.com/owner/repo.git", wantErr: "not a GitHub URL"},
		{name: "https enterprise host is rejected", url: "https://github.mycompany.com/owner/repo.git", wantErr: "not a GitHub URL"},
		{name: "https single path segment", url: "https://github.com/repo", wantErr: "could not extract owner/repo from URL"},

		// git://
		{name: "git protocol", url: "git://github.com/owner/repo.git", wantOwner: "owner", wantRepo: "repo"},
		{name: "git protocol non-GitHub host is rejected", url: "git://example.com/owner/repo.git", wantErr: "not a GitHub URL"},
		{name: "git protocol single path segment", url: "git://github.com/repo", wantErr: "could not extract owner/repo from URL"},

		// token guard
		{name: "embedded credentials are refused", url: "https://user:secret@github.com/owner/repo.git", wantErr: "appears to contain a token"},
		{name: "PAT prefix is refused", url: "https://ghp_abc123@github.com/owner/repo.git", wantErr: "appears to contain a token"},

		// unsupported
		{name: "unknown scheme", url: "ftp://github.com/owner/repo.git", wantErr: "could not parse owner/repo from URL"},
		{name: "empty string", url: "", wantErr: "could not parse owner/repo from URL"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			owner, repo, err := ParseGitURL(tt.url)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				assert.Empty(t, owner)
				assert.Empty(t, repo)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantOwner, owner)
			assert.Equal(t, tt.wantRepo, repo)
		})
	}
}

func TestContainsToken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		url  string
		want bool
	}{
		{name: "clean https URL", url: "https://github.com/owner/repo.git", want: false},
		{name: "clean ssh URL", url: "git@github.com:owner/repo.git", want: false},
		{name: "personal access token", url: "https://ghp_deadbeef@github.com/o/r", want: true},
		{name: "oauth token", url: "https://gho_deadbeef@github.com/o/r", want: true},
		{name: "user token", url: "https://ghu_deadbeef@github.com/o/r", want: true},
		{name: "server token", url: "https://ghs_deadbeef@github.com/o/r", want: true},
		{name: "refresh token", url: "https://ghr_deadbeef@github.com/o/r", want: true},
		{name: "testing token", url: "https://ght_deadbeef@github.com/o/r", want: true},
		{name: "api_token query param", url: "https://github.com/o/r?api_token=x", want: true},
		{name: "access_token query param", url: "https://github.com/o/r?access_token=x", want: true},
		{name: "auth_token query param", url: "https://github.com/o/r?auth_token=x", want: true},
		{name: "embedded user:password", url: "https://user:pass@github.com/o/r", want: true},
		// Uppercase input is lower-cased before matching.
		{name: "uppercase token prefix", url: "https://GHP_DEADBEEF@github.com/o/r", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, containsToken(tt.url))
		})
	}
}

func TestIsGitHubURLHost(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{name: "github.com", raw: "https://github.com/o/r", want: true},
		{name: "uppercase host", raw: "https://GITHUB.COM/o/r", want: true},
		{name: "subdomain of github.com", raw: "https://api.github.com/o/r", want: true},
		{name: "host with port", raw: "https://github.com:443/o/r", want: true},
		{name: "lookalike host", raw: "https://github.com.evil.example/o/r", want: false},
		{name: "enterprise host", raw: "https://github.example.com/o/r", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			u, err := url.Parse(tt.raw)
			require.NoError(t, err)
			assert.Equal(t, tt.want, isGitHubURL(u))
		})
	}
}

func TestIsGitHubURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		url  string
		want bool
	}{
		{name: "https github", url: "https://github.com/o/r", want: true},
		{name: "https other host", url: "https://gitlab.com/o/r", want: false},
		// url.Parse rejects the SSH form (invalid port "o"), so the parse-error
		// branch special-cases the literal "git@github.com:" prefix.
		{name: "ssh github form", url: "git@github.com:o/r.git", want: true},
		{name: "ssh non-github form", url: "git@gitlab.com:o/r.git", want: false},
		{name: "bare owner/repo", url: "o/r", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, IsGitHubURL(tt.url))
		})
	}
}

func TestIsValidGitURL(t *testing.T) {
	t.Parallel()

	assert.True(t, IsValidGitURL("https://github.com/owner/repo.git"))
	assert.True(t, IsValidGitURL("owner/repo"))
	assert.False(t, IsValidGitURL("https://example.com/owner/repo.git"))
}

func TestValidateURLScheme(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		url     string
		wantErr string
	}{
		{name: "ssh", url: "git@github.com:o/r.git"},
		{name: "https", url: "https://github.com/o/r"},
		{name: "http", url: "http://github.com/o/r"},
		{name: "git", url: "git://github.com/o/r"},
		{name: "bare form is unknown", url: "o/r", wantErr: "unknown URL format"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateURLScheme(tt.url)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestCloneURLFromString(t *testing.T) {
	t.Parallel()

	endpoint, err := CloneURLFromString("https://github.com/owner/repo.git")
	require.NoError(t, err)
	assert.Equal(t, "github.com", endpoint.Host)

	// transport.NewEndpoint is deliberately permissive: anything it cannot
	// classify becomes a local file endpoint rather than an error.
	local, err := CloneURLFromString("/srv/git/repo.git")
	require.NoError(t, err)
	assert.Equal(t, "file", local.Protocol)
}
