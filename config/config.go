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

	// Environment variables - bind GITHUB_TOKEN explicitly
	v.BindEnv("token", "GITHUB_TOKEN")
	v.BindEnv("repo_owner", "GH_REPO_OWNER")
	v.BindEnv("repo_name", "GH_REPO_NAME")

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

	// Try to read config file, ignore errors if not found
	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			log.Warnf("Error reading config file: %v", err)
		}
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
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
		return fmt.Errorf("GitHub token is required. Set GITHUB_TOKEN environment variable, or set 'token' in config file")
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
	errStr := err.Error()
	// GitHub returns 404 for private repos when not authenticated
	return strings.Contains(errStr, "404") ||
		strings.Contains(errStr, "401") ||
		strings.Contains(errStr, "Bad credentials") ||
		strings.Contains(errStr, "authentication")
}
