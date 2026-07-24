package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoad_WithConfigFile(t *testing.T) {
	// Create a temporary config file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	configContent := `
token: test-token
repo_owner: test-owner
repo_name: test-repo
log_level: debug
retry_max: 5
artifact_root: /tmp/artifacts
`
	err := os.WriteFile(configPath, []byte(configContent), 0644)
	require.NoError(t, err)

	cfg, err := Load(configPath)
	require.NoError(t, err)

	assert.Equal(t, "test-token", cfg.Token)
	assert.Equal(t, "test-owner", cfg.RepoOwner)
	assert.Equal(t, "test-repo", cfg.RepoName)
	assert.Equal(t, "debug", cfg.LogLevel)
	assert.Equal(t, 5, cfg.RetryMax)
	assert.Equal(t, "/tmp/artifacts", cfg.ArtifactRoot)
}

func TestLoad_EnvOverride(t *testing.T) {
	// Create a temporary config file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	configContent := `
token: file-token
repo_owner: file-owner
repo_name: file-repo
`
	err := os.WriteFile(configPath, []byte(configContent), 0644)
	require.NoError(t, err)

	// Set environment variables
	os.Setenv("GITHUB_TOKEN", "env-token")
	os.Setenv("GH_REPO_OWNER", "env-owner")
	os.Setenv("GH_REPO_NAME", "env-repo")
	defer func() {
		os.Unsetenv("GITHUB_TOKEN")
		os.Unsetenv("GH_REPO_OWNER")
		os.Unsetenv("GH_REPO_NAME")
	}()

	cfg, err := Load(configPath)
	require.NoError(t, err)

	// Environment variables should override config file
	assert.Equal(t, "env-token", cfg.Token)
	assert.Equal(t, "env-owner", cfg.RepoOwner)
	assert.Equal(t, "env-repo", cfg.RepoName)
}

func TestLoad_DefaultValues(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	// Create empty config file
	err := os.WriteFile(configPath, []byte(""), 0644)
	require.NoError(t, err)

	cfg, err := Load(configPath)
	require.NoError(t, err)

	// Check defaults
	assert.Equal(t, "info", cfg.LogLevel)
}

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

func TestLoad_FileNotFound(t *testing.T) {
	// Should not error when config file doesn't exist
	cfg, _ := Load("/nonexistent/path/config.yaml")
	// May error or return empty config depending on viper behavior
	// The important thing is it doesn't panic
	if cfg != nil {
		assert.Empty(t, cfg.Token)
	}
}

// TestLoad_IgnoresCwdConfig verifies that a config.yaml in the current
// working directory is NOT picked up by the default search path. This
// prevents running gh-actions-mcp from inside another project (e.g. one
// with its own config.yaml) from silently shadowing the global gh-proxy
// token in ~/.config/gh-actions-mcp/config.yaml.
func TestLoad_IgnoresCwdConfig(t *testing.T) {
	// Save and restore HOME / cwd so we can isolate the test.
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	cwd, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	// A project-local config.yaml that DOES NOT contain a token.
	projectDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(projectDir, "config.yaml"), []byte("repo_owner: some-owner\n"), 0o644))
	require.NoError(t, os.Chdir(projectDir))

	// A global config that DOES contain a token. It must win.
	globalDir := filepath.Join(tmpHome, ".config", "gh-actions-mcp")
	require.NoError(t, os.MkdirAll(globalDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(globalDir, "config.yaml"), []byte("token: global-token\n"), 0o644))

	cfg, err := Load("")
	require.NoError(t, err)
	assert.Equal(t, "global-token", cfg.Token, "token should come from ~/.config/gh-actions-mcp/config.yaml, not from cwd")
}

func TestSetLogger(t *testing.T) {
	logger := logrus.New()
	logger.SetLevel(logrus.DebugLevel)

	SetLogger(logger)
	// This test mainly ensures the function doesn't panic
}

func TestLoad_GITHUB_PREFIX_EnvVars(t *testing.T) {
	// Test that GITHUB_* prefixed environment variables work
	tests := []struct {
		name          string
		envVars       map[string]string
		expectedToken string
		expectedOwner string
		expectedRepo  string
	}{
		{
			name: "GITHUB_* prefix works",
			envVars: map[string]string{
				"GITHUB_TOKEN":      "github-token",
				"GITHUB_REPO_OWNER": "github-owner",
				"GITHUB_REPO_NAME":  "github-repo",
			},
			expectedToken: "github-token",
			expectedOwner: "github-owner",
			expectedRepo:  "github-repo",
		},
		{
			name: "GH_* prefix works",
			envVars: map[string]string{
				"GH_TOKEN":      "gh-token",
				"GH_REPO_OWNER": "gh-owner",
				"GH_REPO_NAME":  "gh-repo",
			},
			expectedToken: "gh-token",
			expectedOwner: "gh-owner",
			expectedRepo:  "gh-repo",
		},
		{
			name: "GITHUB_* takes precedence over GH_*",
			envVars: map[string]string{
				"GITHUB_TOKEN":      "github-token",
				"GH_TOKEN":          "gh-token",
				"GITHUB_REPO_OWNER": "github-owner",
				"GH_REPO_OWNER":     "gh-owner",
				"GITHUB_REPO_NAME":  "github-repo",
				"GH_REPO_NAME":      "gh-repo",
			},
			expectedToken: "github-token",
			expectedOwner: "github-owner",
			expectedRepo:  "github-repo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set environment variables
			for k, v := range tt.envVars {
				os.Setenv(k, v)
			}
			defer func() {
				for k := range tt.envVars {
					os.Unsetenv(k)
				}
			}()

			cfg, err := Load("")
			require.NoError(t, err)

			assert.Equal(t, tt.expectedToken, cfg.Token)
			assert.Equal(t, tt.expectedOwner, cfg.RepoOwner)
			assert.Equal(t, tt.expectedRepo, cfg.RepoName)
		})
	}
}

func TestLoad_PerPageLimit(t *testing.T) {
	tests := []struct {
		name          string
		configContent string
		envValue      string
		expectedLimit int
	}{
		{
			name:          "default per_page_limit",
			configContent: "",
			envValue:      "",
			expectedLimit: 50,
		},
		{
			name:          "per_page_limit from config file",
			configContent: "per_page_limit: 100",
			envValue:      "",
			expectedLimit: 100,
		},
		{
			name:          "GITHUB_PER_PAGE_LIMIT env var",
			configContent: "",
			envValue:      "75",
			expectedLimit: 75,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			configPath := filepath.Join(tmpDir, "config.yaml")

			if tt.configContent != "" {
				err := os.WriteFile(configPath, []byte(tt.configContent), 0644)
				require.NoError(t, err)
			} else {
				err := os.WriteFile(configPath, []byte(""), 0644)
				require.NoError(t, err)
			}

			if tt.envValue != "" {
				os.Setenv("GITHUB_PER_PAGE_LIMIT", tt.envValue)
				defer os.Unsetenv("GITHUB_PER_PAGE_LIMIT")
			}

			cfg, err := Load(configPath)
			require.NoError(t, err)

			assert.Equal(t, tt.expectedLimit, cfg.PerPageLimit)
		})
	}
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

func TestLoad_GitProxyDetectDefaultsToTrue(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(""), 0644))

	cfg, err := Load(configPath)
	require.NoError(t, err)
	assert.True(t, cfg.GitProxyDetect)
}

func TestLoad_GitProxyDetectFromConfigAndEnv(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte("git_proxy_detect: false\n"), 0644))

	cfg, err := Load(configPath)
	require.NoError(t, err)
	assert.False(t, cfg.GitProxyDetect)

	t.Setenv("GITHUB_GIT_PROXY_DETECT", "true")
	cfg, err = Load(configPath)
	require.NoError(t, err)
	assert.True(t, cfg.GitProxyDetect)
}

func TestLoad_APIBaseURLEnv(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(""), 0644))

	t.Setenv("GH_API_BASE_URL", "http://gh-proxy.local/api/")
	t.Setenv("GH_UPLOAD_URL", "http://gh-proxy.local/api/uploads/")

	cfg, err := Load(configPath)
	require.NoError(t, err)
	assert.Equal(t, "http://gh-proxy.local/api/", cfg.APIBaseURL)
	assert.Equal(t, "http://gh-proxy.local/api/uploads/", cfg.UploadURL)
}

func TestSplitProxyCredentials(t *testing.T) {
	tests := []struct {
		name      string
		cfg       Config
		wantUser  string
		wantToken string
	}{
		{
			name:      "no api base url leaves token alone",
			cfg:       Config{Token: "user.token"},
			wantToken: "user.token",
		},
		{
			name:      "explicit auth username wins",
			cfg:       Config{APIBaseURL: "http://proxy/api/", AuthUsername: "me", Token: "user.token"},
			wantUser:  "me",
			wantToken: "user.token",
		},
		{
			name:      "github PAT is never split",
			cfg:       Config{APIBaseURL: "http://proxy/api/", Token: "ghp_abc.def"},
			wantToken: "ghp_abc.def",
		},
		{
			name:      "fine-grained PAT is never split",
			cfg:       Config{APIBaseURL: "http://proxy/api/", Token: "github_pat_abc.def"},
			wantToken: "github_pat_abc.def",
		},
		{
			name:      "proxy credential is split on first dot",
			cfg:       Config{APIBaseURL: "http://proxy/api/", Token: "workspace.s3cr3t.with.dots"},
			wantUser:  "workspace",
			wantToken: "s3cr3t.with.dots",
		},
		{
			name:      "token without dot is left alone",
			cfg:       Config{APIBaseURL: "http://proxy/api/", Token: "nodotshere"},
			wantToken: "nodotshere",
		},
		{
			name:      "empty user side is left alone",
			cfg:       Config{APIBaseURL: "http://proxy/api/", Token: ".onlytoken"},
			wantToken: ".onlytoken",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.cfg
			cfg.splitProxyCredentials()
			assert.Equal(t, tt.wantUser, cfg.AuthUsername)
			assert.Equal(t, tt.wantToken, cfg.Token)
		})
	}
}
