package config

import (
	"fmt"
	"runtime"
	"strings"

	"github.com/sirupsen/logrus"
	"github.com/spf13/viper"
)

type Config struct {
	Token         string `mapstructure:"token"`
	RepoOwner     string `mapstructure:"repo_owner"`
	RepoName      string `mapstructure:"repo_name"`
	LogLevel      string `mapstructure:"log_level"`
	DefaultLimit  int    `mapstructure:"default_limit"`
	DefaultLogLen int    `mapstructure:"default_log_len"`
	PerPageLimit  int    `mapstructure:"per_page_limit"`
	DefaultFormat string `mapstructure:"default_format"` // "minimal", "compact", "full"
}

var log = logrus.New()

func SetLogger(l *logrus.Logger) {
	log = l
}

func Load(configPath string) (*Config, error) {
	v := viper.New()

	// Set defaults
	v.SetDefault("log_level", "info")
	v.SetDefault("token", "")
	v.SetDefault("default_limit", 10)
	v.SetDefault("default_log_len", 100)
	v.SetDefault("per_page_limit", 50)
	v.SetDefault("default_format", "compact")

	// Environment variables - support both GITHUB_* and GH_* prefixes
	// GITHUB_* prefix takes precedence over GH_* prefix for backward compatibility
	_ = v.BindEnv("token", "GITHUB_TOKEN", "GH_TOKEN")
	_ = v.BindEnv("repo_owner", "GITHUB_REPO_OWNER", "GH_REPO_OWNER")
	_ = v.BindEnv("repo_name", "GITHUB_REPO_NAME", "GH_REPO_NAME")
	_ = v.BindEnv("log_level", "GITHUB_LOG_LEVEL", "GH_LOG_LEVEL")
	_ = v.BindEnv("default_limit", "GITHUB_DEFAULT_LIMIT", "GH_DEFAULT_LIMIT")
	_ = v.BindEnv("default_log_len", "GITHUB_DEFAULT_LOG_LEN", "GH_DEFAULT_LOG_LEN")
	_ = v.BindEnv("per_page_limit", "GITHUB_PER_PAGE_LIMIT", "GH_PER_PAGE_LIMIT")
	_ = v.BindEnv("default_format", "GITHUB_DEFAULT_FORMAT", "GH_DEFAULT_FORMAT")

	// Config file
	if configPath != "" {
		v.SetConfigFile(configPath)
	} else {
		v.SetConfigName("config")
		v.SetConfigType("yaml")
		v.AddConfigPath(".")
		v.AddConfigPath("$HOME/.config/gh-actions-mcp")
		v.AddConfigPath("/etc/gh-actions-mcp")
	}

	// Try to read config file, provide clearer error messages
	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); ok {
			// Config file not found is OK - will use defaults and env vars
			log.Debugf("No config file found, using defaults and environment variables")
		} else if configErr, ok := err.(viper.ConfigParseError); ok {
			// Config file exists but has invalid syntax - provide clearer error
			return nil, fmt.Errorf("config file syntax error: %w\nEnsure your config file is valid YAML format", configErr)
		} else {
			// Other error (permissions, etc.) - provide clearer error
			return nil, fmt.Errorf("failed to read config file: %w\nCheck file permissions and path", err)
		}
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("config file validation error: %w\nEnsure all config values have correct types (strings, numbers, etc.)", err)
	}

	// Override with environment variable if set
	if token := v.GetString("token"); token != "" {
		cfg.Token = token
	}

	log.Debugf("Loaded config: owner=%s, repo=%s", cfg.RepoOwner, cfg.RepoName)
	return &cfg, nil
}

func (c *Config) Validate() error {
	if c.Token == "" {
		// Try to get token from macOS keychain (only on macOS)
		if runtime.GOOS == "darwin" {
			if token, err := getTokenFromKeychain(); err == nil {
				c.Token = token
				log.Infof("Obtained GitHub token from macOS keychain")
			} else {
				log.Debugf("Could not get token from keychain: %v", err)
			}
		}
	}

	if c.Token == "" {
		return fmt.Errorf("GitHub token is required. Set GITHUB_TOKEN environment variable, set 'token' in config file, or run 'gh auth login' on macOS")
	}
	if c.RepoOwner == "" {
		return fmt.Errorf("repository owner is required. Set GH_REPO_OWNER env var, 'repo_owner' in config, or use --repo-owner flag")
	}
	if c.RepoName == "" {
		return fmt.Errorf("repository name is required. Set GH_REPO_NAME env var, 'repo_name' in config, or use --repo-name flag")
	}
	return nil
}

// IsAuthenticationError checks if an error is likely related to authentication
func IsAuthenticationError(err error) bool {
	if err == nil {
		return false
	}
	errStr := strings.ToLower(err.Error())
	// GitHub returns 404 for private repos when not authenticated
	return strings.Contains(errStr, "404") ||
		strings.Contains(errStr, "401") ||
		strings.Contains(errStr, "403") ||
		strings.Contains(errStr, "bad credentials") ||
		strings.Contains(errStr, "authentication") ||
		strings.Contains(errStr, "resource not accessible by personal access token") ||
		strings.Contains(errStr, "insufficient permission") ||
		strings.Contains(errStr, "forbidden")
}
