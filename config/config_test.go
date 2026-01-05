package config

import (
	"os"
	"path/filepath"
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
`
	err := os.WriteFile(configPath, []byte(configContent), 0644)
	require.NoError(t, err)

	cfg, err := Load(configPath)
	require.NoError(t, err)

	assert.Equal(t, "test-token", cfg.Token)
	assert.Equal(t, "test-owner", cfg.RepoOwner)
	assert.Equal(t, "test-repo", cfg.RepoName)
	assert.Equal(t, "debug", cfg.LogLevel)
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

func TestLoad_FileNotFound(t *testing.T) {
	// Should not error when config file doesn't exist
	cfg, _ := Load("/nonexistent/path/config.yaml")
	// May error or return empty config depending on viper behavior
	// The important thing is it doesn't panic
	if cfg != nil {
		assert.Empty(t, cfg.Token)
	}
}

func TestSetLogger(t *testing.T) {
	logger := logrus.New()
	logger.SetLevel(logrus.DebugLevel)

	SetLogger(logger)
	// This test mainly ensures the function doesn't panic
}
