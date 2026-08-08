package cmd

import (
	"fmt"

	"github.com/denysvitali/gh-actions-mcp/config"
	"github.com/denysvitali/gh-actions-mcp/github"

	"github.com/sirupsen/logrus"
)

// loadConfig resolves the effective configuration and fails unless a default
// repository is known.
func loadConfig() (*config.Config, error) {
	cfg, err := loadResolvedConfig()
	if err != nil {
		return nil, err
	}
	if cfg.RepoOwner == "" {
		return nil, fmt.Errorf("repository owner is required. Set GH_REPO_OWNER env var, 'repo_owner' in config, or use --repo-owner flag")
	}
	if cfg.RepoName == "" {
		return nil, fmt.Errorf("repository name is required. Set GH_REPO_NAME env var, 'repo_name' in config, or use --repo-name flag")
	}
	logConfiguredRepository(cfg)
	return cfg, nil
}

// loadConfigAllowMissingRepo resolves the effective configuration for commands
// that take owner/repo per call (the `tool` runner), so a default repository is
// optional.
func loadConfigAllowMissingRepo() (*config.Config, error) {
	cfg, err := loadResolvedConfig()
	if err != nil {
		return nil, err
	}
	logConfiguredRepository(cfg)
	return cfg, nil
}

// loadResolvedConfig is the work shared by loadConfig and
// loadConfigAllowMissingRepo: read the config file, layer the CLI flags on top,
// infer the repository, resolve proxy credentials, and validate the token. The
// two callers differ only in whether a missing repository is fatal.
func loadResolvedConfig() (*config.Config, error) {
	config.SetLogger(log)

	cfg, err := config.Load(cfgFile)
	if err != nil {
		return nil, err
	}

	// Override with CLI flags
	if repoOwner != "" {
		cfg.RepoOwner = repoOwner
	}
	if repoName != "" {
		cfg.RepoName = repoName
	}
	if token != "" {
		cfg.Token = token
	}
	if logLevel != "" {
		cfg.LogLevel = logLevel
	}
	cfg.ServerVersion = version

	// Try to infer repo from git if not set
	if cfg.RepoOwner == "" || cfg.RepoName == "" {
		if inferErr := inferRepoFromGit(cfg); inferErr != nil {
			log.Warnf("Could not infer repo from git: %v", inferErr)
		}
	}

	// Credentials embedded in the proxy rewrite can be the token. Run before
	// and after detection: detection may set the API base URL (enabling the
	// split), and the split may add a username that detection must respect.
	cfg.SplitProxyCredentials()

	// Route API calls through the same proxy git uses, if any. Must run
	// before ValidateToken: the proxy credentials can be the only token.
	if noGitProxyDetect {
		cfg.GitProxyDetect = false
	}
	applyGitProxyDetection(cfg)
	cfg.SplitProxyCredentials()

	if err := cfg.ValidateToken(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func logConfiguredRepository(cfg *config.Config) {
	if cfg.RepoOwner != "" && cfg.RepoName != "" {
		log.Infof("Configured for repository: %s/%s", cfg.RepoOwner, cfg.RepoName)
	} else {
		log.Infof("Configured without a default repository")
	}
}

func configureLogLevel() error {
	level, err := logrus.ParseLevel(logLevel)
	if err != nil {
		return fmt.Errorf("invalid log level: %w", err)
	}
	log.SetLevel(level)
	return nil
}

// applyGitProxyDetection derives the API base URL (and HTTP Basic credentials)
// from git's url.<proxy>.insteadOf rewrites, so that a machine whose git only
// reaches github.com through a proxy such as gh-proxy also reaches the REST
// API through it. Explicit configuration always wins; credentials are never
// logged.
func applyGitProxyDetection(cfg *config.Config) {
	if !cfg.GitProxyDetect {
		log.Debugf("Git proxy detection disabled")
		return
	}
	if cfg.APIBaseURL != "" {
		log.Debugf("api_base_url already set, skipping git proxy detection")
		return
	}

	proxy, err := github.DetectProxy("")
	if err != nil {
		log.Debugf("Could not read git config for proxy detection: %v", err)
		return
	}
	if proxy == nil {
		return
	}

	cfg.APIBaseURL = proxy.APIBaseURL
	switch {
	case cfg.AuthUsername != "":
		// The user configured Basic auth themselves; keep their credentials.
	case proxy.HasCredentials():
		// A GitHub PAT is useless against a proxy that authenticates its own
		// consumers, so the credentials from git config take precedence.
		cfg.AuthUsername = proxy.Username
		cfg.Token = proxy.Password
	}
	log.Infof("Using GitHub proxy from git config: %s", proxy)
}

func inferRepoFromGit(cfg *config.Config) error {
	detector := github.NewRepoDetector()
	info, err := detector.Detect()
	if err != nil {
		return err
	}

	if cfg.RepoOwner == "" {
		cfg.RepoOwner = info.Owner
	}
	if cfg.RepoName == "" {
		cfg.RepoName = info.Repo
	}

	log.Infof("Inferred repository from %s: %s/%s", info.Source, info.Owner, info.Repo)
	return nil
}
