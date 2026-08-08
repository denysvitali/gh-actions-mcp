package config

import (
	"errors"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfig_Validate(t *testing.T) {
	originalProvider := keychainTokenProvider
	originalGHProvider := ghTokenProvider
	keychainTokenProvider = func() (string, error) {
		return "", errors.New("no token in test keychain")
	}
	ghTokenProvider = func() (string, error) {
		return "", errors.New("not logged in during test")
	}
	t.Cleanup(func() {
		keychainTokenProvider = originalProvider
		ghTokenProvider = originalGHProvider
	})

	tests := []struct {
		name      string
		cfg       Config
		wantError bool
	}{
		{
			name: "valid config",
			cfg: Config{
				Token:     "token",
				RepoOwner: "owner",
				RepoName:  "repo",
			},
			wantError: false,
		},
		{
			name: "missing token",
			cfg: Config{
				Token:     "",
				RepoOwner: "owner",
				RepoName:  "repo",
			},
			wantError: true,
		},
		{
			name: "missing owner",
			cfg: Config{
				Token:     "token",
				RepoOwner: "",
				RepoName:  "repo",
			},
			wantError: true,
		},
		{
			name: "missing name",
			cfg: Config{
				Token:     "token",
				RepoOwner: "owner",
				RepoName:  "",
			},
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if tt.wantError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestConfig_ValidateToken(t *testing.T) {
	originalProvider := keychainTokenProvider
	originalGHProvider := ghTokenProvider
	keychainTokenProvider = func() (string, error) {
		return "", errors.New("no token in test keychain")
	}
	ghTokenProvider = func() (string, error) { return "", errors.New("not logged in") }
	t.Cleanup(func() {
		keychainTokenProvider = originalProvider
		ghTokenProvider = originalGHProvider
	})

	cfg := Config{Token: "token"}
	require.NoError(t, cfg.ValidateToken())

	cfg.Token = ""
	require.Error(t, cfg.ValidateToken())
}

func TestConfig_ValidateToken_UsesGHCLIFallback(t *testing.T) {
	originalProvider, originalKeychainProvider := ghTokenProvider, keychainTokenProvider
	keychainTokenProvider = func() (string, error) { return "", errors.New("no keychain token") }
	ghTokenProvider = func() (string, error) { return "gho_test-from-cli", nil }
	t.Cleanup(func() {
		ghTokenProvider = originalProvider
		keychainTokenProvider = originalKeychainProvider
	})

	cfg := Config{}
	require.NoError(t, cfg.ValidateToken())
	assert.Equal(t, "gho_test-from-cli", cfg.Token)
}

func TestConfig_Validate_UsesKeychainProvider(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("keychain provider is only used on macOS")
	}

	originalProvider := keychainTokenProvider
	keychainTokenProvider = func() (string, error) {
		return "gho_test-from-keychain", nil
	}
	t.Cleanup(func() {
		keychainTokenProvider = originalProvider
	})

	cfg := Config{
		RepoOwner: "owner",
		RepoName:  "repo",
	}

	err := cfg.Validate()
	require.NoError(t, err)
	assert.Equal(t, "gho_test-from-keychain", cfg.Token)
}

func TestIsAuthenticationError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "401 unauthorized",
			err:  errors.New("failed request: HTTP 401"),
			want: true,
		},
		{
			name: "403 pat limitation",
			err:  errors.New("403 Resource not accessible by personal access token"),
			want: true,
		},
		{
			name: "forbidden text",
			err:  errors.New("forbidden by policy"),
			want: true,
		},
		{
			name: "non-auth error",
			err:  errors.New("validation failed"),
			want: false,
		},
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsAuthenticationError(tt.err))
		})
	}
}
