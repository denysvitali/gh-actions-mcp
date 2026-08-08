package config

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/sirupsen/logrus"
	"github.com/spf13/viper"
)

// Config is the fully resolved configuration. Every field is set by [Load]
// (from a default, the config file, or the environment) before the value is
// handed out, so a caller never has to distinguish "unset" from "zero" for a
// field that has a default. It carries no locks: see the concurrency note in
// the package documentation.
type Config struct {
	// ServerVersion is the version string reported over MCP. It is not read
	// from configuration (mapstructure:"-"); package cmd sets it from the
	// build version.
	ServerVersion string `mapstructure:"-"`
	// Token is the credential used against the GitHub API — a GitHub token, or
	// the password half of HTTP Basic auth when AuthUsername is set. Guaranteed
	// non-empty only after [Config.ValidateToken] returns nil.
	Token string `mapstructure:"token"`
	// RepoOwner is the default repository owner. May be empty: tools that take
	// an explicit owner do not need it.
	RepoOwner string `mapstructure:"repo_owner"`
	// RepoName is the default repository name, with the same caveat as
	// RepoOwner.
	RepoName string `mapstructure:"repo_name"`
	// LogLevel is a logrus level name (debug, info, warn, error). Defaults to
	// "info"; guaranteed non-empty.
	LogLevel string `mapstructure:"log_level"`
	// DefaultLimit is how many items a list tool returns when the caller does
	// not say. Defaults to 10; guaranteed positive.
	DefaultLimit int `mapstructure:"default_limit"`
	// DefaultLogLen is how many log lines are returned when the caller does not
	// say. Defaults to 100; guaranteed positive.
	DefaultLogLen int `mapstructure:"default_log_len"`
	// PerPageLimit is the page size requested from the GitHub API. Defaults to
	// 50; guaranteed positive. GitHub itself caps this at 100.
	PerPageLimit int `mapstructure:"per_page_limit"`
	// RetryMax is how many times a safe (idempotent) GitHub read is retried
	// after a transient failure. Defaults to 3; -1 disables retries entirely.
	RetryMax int `mapstructure:"retry_max"`
	// ArtifactRoot is the only directory download_artifact may write beneath.
	// Defaults to "."; guaranteed non-empty, so there is always a boundary to
	// enforce.
	ArtifactRoot string `mapstructure:"artifact_root"`
	// DefaultFormat selects the verbosity of formatted results: "minimal",
	// "compact" or "full". Defaults to "compact"; guaranteed non-empty.
	DefaultFormat string `mapstructure:"default_format"` // "minimal", "compact", "full"
	// AuthUsername, when non-empty, switches from Bearer token auth to
	// HTTP Basic auth (username:token). Required by some reverse proxies
	// (e.g. gh-proxy). Env: GITHUB_AUTH_USERNAME / GH_AUTH_USERNAME.
	AuthUsername string `mapstructure:"auth_username"`
	// APIBaseURL overrides the GitHub API base URL. Useful for GitHub
	// Enterprise or a reverse proxy (e.g. "http://gh-proxy:8080/api/").
	// Must end with a trailing slash.
	APIBaseURL string `mapstructure:"api_base_url"`
	// UploadURL overrides the GitHub upload URL. Defaults to APIBaseURL
	// when empty.
	UploadURL string `mapstructure:"upload_url"`
	// GitProxyDetect enables deriving api_base_url and Basic-auth
	// credentials from git's url.<proxy>.insteadOf rewrites (e.g. gh-proxy)
	// when no api_base_url is configured. Defaults to true.
	// Env: GITHUB_GIT_PROXY_DETECT / GH_GIT_PROXY_DETECT
	GitProxyDetect bool `mapstructure:"git_proxy_detect"`
}

var (
	log                   = logrus.New()
	keychainTokenProvider = getTokenFromKeychain
	ghTokenProvider       = getTokenFromGHCLI
)

func getTokenFromGHCLI() (string, error) {
	// noctx wants exec.CommandContext, but the only caller chain is
	// Config.ValidateToken, which is exported and takes no context. Passing
	// context.Background() here would satisfy the linter without giving anyone
	// the ability to cancel, so the honest answer is to say so.
	output, err := exec.Command("gh", "auth", "token").Output() //nolint:noctx
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

// SetLogger replaces the logger this package writes to. It guarantees nothing
// about concurrent use: call it once during startup, before any goroutine that
// can trigger a config operation exists.
func SetLogger(l *logrus.Logger) {
	log = l
}

// Load resolves the effective configuration from defaults, a YAML config file
// and the environment, in that order of increasing precedence. A missing config
// file is not an error; a malformed one is. The returned Config always has
// every default applied, so callers never see a zero value for a field that has
// a default.
func Load(configPath string) (*Config, error) {
	v := viper.New()
	setDefaults(v)
	bindEnvVars(v)

	if err := readConfigFile(v, configPath); err != nil {
		return nil, err
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("config file validation error: %w\nEnsure all config values have correct types (strings, numbers, etc.)", err)
	}

	// Override with environment variable if set
	if token := v.GetString("token"); token != "" {
		cfg.Token = token
	}

	cfg.splitProxyCredentials()

	log.Debugf("Loaded config: owner=%s, repo=%s", cfg.RepoOwner, cfg.RepoName)
	return &cfg, nil
}

// setDefaults declares the value every key takes when neither the config file
// nor the environment sets it.
func setDefaults(v *viper.Viper) {
	v.SetDefault("log_level", "info")
	v.SetDefault("token", "")
	v.SetDefault("default_limit", 10)
	v.SetDefault("default_log_len", 100)
	v.SetDefault("per_page_limit", 50)
	v.SetDefault("retry_max", 3)
	v.SetDefault("artifact_root", ".")
	v.SetDefault("default_format", "compact")
	v.SetDefault("git_proxy_detect", true)
}

// bindEnvVars binds every config key to its environment variables.
// Both GITHUB_* and GH_* prefixes are supported; the GITHUB_* name is listed
// first, and viper takes the first name that is set, so GITHUB_* wins.
func bindEnvVars(v *viper.Viper) {
	_ = v.BindEnv("token", "GITHUB_TOKEN", "GH_TOKEN")
	_ = v.BindEnv("repo_owner", "GITHUB_REPO_OWNER", "GH_REPO_OWNER")
	_ = v.BindEnv("repo_name", "GITHUB_REPO_NAME", "GH_REPO_NAME")
	_ = v.BindEnv("log_level", "GITHUB_LOG_LEVEL", "GH_LOG_LEVEL")
	_ = v.BindEnv("default_limit", "GITHUB_DEFAULT_LIMIT", "GH_DEFAULT_LIMIT")
	_ = v.BindEnv("default_log_len", "GITHUB_DEFAULT_LOG_LEN", "GH_DEFAULT_LOG_LEN")
	_ = v.BindEnv("per_page_limit", "GITHUB_PER_PAGE_LIMIT", "GH_PER_PAGE_LIMIT")
	_ = v.BindEnv("retry_max", "GITHUB_RETRY_MAX", "GH_RETRY_MAX")
	_ = v.BindEnv("artifact_root", "GITHUB_ARTIFACT_ROOT", "GH_ARTIFACT_ROOT")
	_ = v.BindEnv("default_format", "GITHUB_DEFAULT_FORMAT", "GH_DEFAULT_FORMAT")
	_ = v.BindEnv("auth_username", "GITHUB_AUTH_USERNAME", "GH_AUTH_USERNAME")
	_ = v.BindEnv("api_base_url", "GITHUB_API_BASE_URL", "GH_API_BASE_URL")
	_ = v.BindEnv("upload_url", "GITHUB_UPLOAD_URL", "GH_UPLOAD_URL")
	_ = v.BindEnv("git_proxy_detect", "GITHUB_GIT_PROXY_DETECT", "GH_GIT_PROXY_DETECT")
}

// readConfigFile points viper at the config file and reads it. Two modes:
//
//  1. Explicit path via --config / configPath: load that single file.
//  2. Default search: only the dedicated gh-actions-mcp config directories,
//     in priority order. "." is intentionally NOT searched, because running
//     from a project's working directory (e.g. from inside another repo)
//     would otherwise pick up that project's own config.yaml and silently
//     ignore the global config. Use --config / -c for a project-local file.
//
// A missing file is not an error: defaults and environment variables stand on
// their own.
func readConfigFile(v *viper.Viper, configPath string) error {
	if configPath != "" {
		v.SetConfigFile(configPath)
	} else {
		v.SetConfigName("config")
		v.SetConfigType("yaml")
		v.AddConfigPath("$HOME/.config/gh-actions-mcp")
		v.AddConfigPath("/etc/gh-actions-mcp")
	}

	err := v.ReadInConfig()
	if err == nil {
		return nil
	}

	var notFound viper.ConfigFileNotFoundError
	var parseErr viper.ConfigParseError
	switch {
	case errors.As(err, &notFound):
		log.Debugf("No config file found, using defaults and environment variables")
		return nil
	case errors.As(err, &parseErr):
		return fmt.Errorf("config file syntax error: %w\nEnsure your config file is valid YAML format", parseErr)
	default:
		return fmt.Errorf("failed to read config file: %w\nCheck file permissions and path", err)
	}
}

// splitProxyCredentials handles tokens supplied as "username.token" against a
// reverse proxy that requires HTTP Basic auth (gh-proxy). Real GitHub tokens
// carry a known prefix and never need splitting, so this only applies when a
// custom api_base_url is configured, no auth_username was given, and the token
// does not look like a GitHub-issued token.
func (c *Config) splitProxyCredentials() {
	if c.APIBaseURL == "" || c.AuthUsername != "" || c.Token == "" {
		return
	}
	for _, prefix := range []string{"ghp_", "gho_", "ghu_", "ghs_", "ghr_", "github_pat_"} {
		if strings.HasPrefix(c.Token, prefix) {
			return
		}
	}
	user, token, found := strings.Cut(c.Token, ".")
	if !found || user == "" || token == "" {
		return
	}
	c.AuthUsername = user
	c.Token = token
	log.Debugf("Split proxy credentials from token: username=%s", user)
}

// SplitProxyCredentials is the exported variant of splitProxyCredentials for
// callers that set APIBaseURL after Load returned (e.g. CLI flag overrides or
// git proxy detection ordering).
//
// It guarantees idempotence: calling it twice is the same as calling it once,
// which is what lets package cmd run it both before and after git proxy
// detection. It never clears a field, and it never touches a token that carries
// a GitHub prefix.
func (c *Config) SplitProxyCredentials() {
	c.splitProxyCredentials()
}
