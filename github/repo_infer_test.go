package github

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestInferRepoFromOrigin_HTTPS(t *testing.T) {
	tests := []struct {
		name      string
		url       string
		wantOwner string
		wantRepo  string
		wantErr   bool
	}{
		{
			name:      "HTTPS URL",
			url:       "https://github.com/owner/repo.git",
			wantOwner: "owner",
			wantRepo:  "repo",
			wantErr:   false,
		},
		{
			name:      "HTTPS URL without .git",
			url:       "https://github.com/owner/repo",
			wantOwner: "owner",
			wantRepo:  "repo",
			wantErr:   false,
		},
		{
			name:      "HTTP URL",
			url:       "http://github.com/owner/repo.git",
			wantOwner: "owner",
			wantRepo:  "repo",
			wantErr:   false,
		},
		// Note: Non-github.com URLs will fail as expected
		{
			name:      "Non-GitHub URL fails",
			url:       "https://github.mycompany.com/owner/repo.git",
			wantOwner: "",
			wantRepo:  "",
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			owner, repo, err := InferRepoFromOrigin(tt.url)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.wantOwner, owner)
				assert.Equal(t, tt.wantRepo, repo)
			}
		})
	}
}

func TestInferRepoFromOrigin_SSH(t *testing.T) {
	tests := []struct {
		name      string
		url       string
		wantOwner string
		wantRepo  string
		wantErr   bool
	}{
		{
			name:      "SSH URL",
			url:       "git@github.com:owner/repo.git",
			wantOwner: "owner",
			wantRepo:  "repo",
			wantErr:   false,
		},
		{
			name:      "SSH URL without .git",
			url:       "git@github.com:owner/repo",
			wantOwner: "owner",
			wantRepo:  "repo",
			wantErr:   false,
		},
		{
			name:      "SSH enterprise URL",
			url:       "git@github.mycompany.com:owner/repo.git",
			wantOwner: "owner",
			wantRepo:  "repo",
			wantErr:   false,
		},
		{
			name:      "SSH URL with extra slash after colon",
			url:       "git@github.com:/owner/repo.git",
			wantOwner: "owner",
			wantRepo:  "repo",
			wantErr:   false,
		},
		{
			name:      "SSH URL with extra slash after colon without .git",
			url:       "git@github.com:/owner/repo",
			wantOwner: "owner",
			wantRepo:  "repo",
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			owner, repo, err := InferRepoFromOrigin(tt.url)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.wantOwner, owner)
				assert.Equal(t, tt.wantRepo, repo)
			}
		})
	}
}

func TestInferRepoFromOrigin_BareFormat(t *testing.T) {
	tests := []struct {
		name      string
		url       string
		wantOwner string
		wantRepo  string
		wantErr   bool
	}{
		{
			name:      "Bare owner/repo format",
			url:       "palantir/policy-bot",
			wantOwner: "palantir",
			wantRepo:  "policy-bot",
			wantErr:   false,
		},
		{
			name:      "Bare owner/repo with underscore",
			url:       "owner_name/repo_name",
			wantOwner: "owner_name",
			wantRepo:  "repo_name",
			wantErr:   false,
		},
		{
			name:      "Bare owner/repo with hyphen",
			url:       "my-org/my-repo",
			wantOwner: "my-org",
			wantRepo:  "my-repo",
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			owner, repo, err := InferRepoFromOrigin(tt.url)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.wantOwner, owner)
				assert.Equal(t, tt.wantRepo, repo)
			}
		})
	}
}

func TestInferRepoFromOrigin_Invalid(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{
			name: "Not a GitHub URL",
			url:  "https://gitlab.com/owner/repo.git",
		},
		{
			name: "Malformed URL",
			url:  "not-a-url",
		},
		{
			name: "Empty string",
			url:  "",
		},
		{
			name: "SSH with wrong format",
			url:  "git@github.com:missing-slash",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := InferRepoFromOrigin(tt.url)
			assert.Error(t, err)
		})
	}
}

// TestInferRepoFromOrigin_DivergesFromParseGitURL pins the CURRENT, BUGGY
// behaviour of the HTTPS branch: it strips the literal prefix "github.com/"
// rather than validating the host, so a non-GitHub host survives into the path
// and becomes the owner whenever exactly two segments remain. It also lacks
// ParseGitURL's containsToken guard and its git:// support.
//
// See FINDINGS.md#2. Do not "fix" these expectations — route
// InferRepoFromOrigin through ParseGitURL in a separate fix: commit and update
// this test then. Merging the two functions during a refactor is a behaviour
// change and is explicitly out of scope.
func TestInferRepoFromOrigin_DivergesFromParseGitURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		url  string
		// what InferRepoFromOrigin does today
		inferOwner string
		inferRepo  string
		inferErr   bool
		// what ParseGitURL does with the same input
		parseErr bool
	}{
		{
			name:       "non-GitHub two-segment host becomes the owner",
			url:        "https://example.com/repo.git",
			inferOwner: "example.com",
			inferRepo:  "repo",
			parseErr:   true,
		},
		{
			name:     "non-GitHub three-segment path errors in both",
			url:      "https://gitlab.com/owner/repo.git",
			inferErr: true,
			parseErr: true,
		},
		{
			name:       "token-bearing URL is accepted by Infer, refused by Parse",
			url:        "https://ghp_deadbeef@github.com/repo.git",
			inferOwner: "ghp_deadbeef@github.com",
			inferRepo:  "repo",
			parseErr:   true,
		},
		{
			name:     "git:// is unsupported by Infer, supported by Parse",
			url:      "git://github.com/owner/repo.git",
			inferErr: true,
			parseErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			owner, repo, err := InferRepoFromOrigin(tt.url)
			if tt.inferErr {
				assert.Error(t, err, "InferRepoFromOrigin")
			} else {
				assert.NoError(t, err, "InferRepoFromOrigin")
				assert.Equal(t, tt.inferOwner, owner)
				assert.Equal(t, tt.inferRepo, repo)
			}

			_, _, parseErr := ParseGitURL(tt.url)
			if tt.parseErr {
				assert.Error(t, parseErr, "ParseGitURL")
			} else {
				assert.NoError(t, parseErr, "ParseGitURL")
			}
		})
	}
}

// TestInferRepoFromOrigin_SharedBranchesMatchParseGitURL pins the branches that
// are byte-identical between InferRepoFromOrigin and ParseGitURL today. These
// are the ones safe to factor into a shared helper.
func TestInferRepoFromOrigin_SharedBranchesMatchParseGitURL(t *testing.T) {
	t.Parallel()

	urls := []string{
		"owner/repo",
		"owner/repo.git",
		"repo",
		"a/b/c",
		"git@github.com:owner/repo.git",
		"git@github.com:owner/repo",
		"git@github.com:/owner/repo.git",
		"git@github.example.com:owner/repo.git",
		"git@github.com:owner/deep/repo.git",
	}

	for _, u := range urls {
		t.Run(u, func(t *testing.T) {
			t.Parallel()

			inferOwner, inferRepo, inferErr := InferRepoFromOrigin(u)
			parseOwner, parseRepo, parseErr := ParseGitURL(u)

			assert.Equal(t, parseOwner, inferOwner)
			assert.Equal(t, parseRepo, inferRepo)
			assert.Equal(t, parseErr == nil, inferErr == nil)
		})
	}
}
