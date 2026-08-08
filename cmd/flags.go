package cmd

import "github.com/spf13/cobra"

// Cobra binds flags to addresses, so every flag value is a package-level var.
// That is the cobra idiom, not an accident: the variables are written once by
// flag parsing before any command runs, and read-only afterwards. Tests that
// set them must not run in parallel (see preserveCommandGlobals).
var (
	version        = "dev"
	cfgFile        string
	repoOwner      string
	repoName       string
	token          string
	logLevel       string
	mcpTransport   string
	mcpHTTPAddress string
	mcpHTTPPath    string
	mcpHTTPToken   string
	mcpHTTPTLSCert string
	mcpHTTPTLSKey  string
	mcpHTTPOrigins []string
	mcpHTTPMaxBody int64
	// noGitProxyDetect disables deriving api_base_url from git insteadOf rules.
	noGitProxyDetect bool
)

// Logs command flags
var (
	logsSearch    string
	logsRegex     string
	logsSection   string
	logsContext   int
	logsTail      int
	logsHead      int
	logsOffset    int
	logsNoHeaders bool
	logsJobID     int64
	logsOwner     string
	logsRepo      string
)

var toolArgsJSON string

// registerRootFlags declares every persistent flag on the root command. Flag
// names, shorthands, defaults and help strings are part of the CLI contract.
func registerRootFlags(cmd *cobra.Command) {
	flags := cmd.PersistentFlags()
	flags.StringVarP(&cfgFile, "config", "c", "", "config file path")
	flags.StringVarP(&repoOwner, "repo-owner", "o", "", "repository owner")
	flags.StringVarP(&repoName, "repo-name", "r", "", "repository name")
	flags.StringVarP(&token, "token", "t", "", "GitHub token (or use GITHUB_TOKEN env var, or macOS keychain)")
	flags.StringVarP(&logLevel, "log-level", "l", "info", "log level (debug, info, warn, error)")
	flags.StringVar(&mcpTransport, "transport", "stdio", "MCP transport: stdio or http")
	flags.StringVar(&mcpHTTPAddress, "http-address", "127.0.0.1:8080", "Streamable HTTP listen address")
	flags.StringVar(&mcpHTTPPath, "http-path", "/mcp", "Streamable HTTP endpoint path")
	flags.StringVar(&mcpHTTPToken, "http-token", "", "Bearer token required by the Streamable HTTP endpoint (or GH_ACTIONS_MCP_HTTP_TOKEN)")
	flags.StringVar(&mcpHTTPTLSCert, "http-tls-cert", "", "TLS certificate for Streamable HTTP")
	flags.StringVar(&mcpHTTPTLSKey, "http-tls-key", "", "TLS private key for Streamable HTTP")
	flags.StringSliceVar(&mcpHTTPOrigins, "http-allowed-origin", nil, "trusted browser origin (repeatable)")
	flags.Int64Var(&mcpHTTPMaxBody, "http-max-body", 1<<20, "maximum Streamable HTTP request body in bytes")
	flags.BoolVar(&noGitProxyDetect, "no-git-proxy-detect", false, "do not derive the API base URL from git url.<proxy>.insteadOf rules")
}

// registerLogsFlags declares the flags of the `logs` subcommand.
func registerLogsFlags(cmd *cobra.Command) {
	flags := cmd.Flags()
	flags.StringVarP(&logsSearch, "search", "s", "", "Filter lines containing substring")
	flags.StringVar(&logsRegex, "regex", "", "Filter lines matching regex pattern")
	flags.StringVar(&logsSection, "section", "", "Extract a specific section by name/pattern")
	flags.IntVarP(&logsContext, "context", "C", 0, "Show N lines of context around matches")
	flags.IntVar(&logsTail, "tail", 0, "Show last N lines")
	flags.IntVar(&logsHead, "head", 0, "Show first N lines")
	flags.IntVar(&logsOffset, "offset", 0, "Skip first N lines")
	flags.BoolVar(&logsNoHeaders, "no-headers", false, "Don't print file headers")
	flags.Int64VarP(&logsJobID, "job-id", "j", 0, "Specific job ID (when using run ID)")
	flags.StringVar(&logsOwner, "owner", "", "Override repo owner")
	flags.StringVar(&logsRepo, "repo", "", "Override repo name")
}

// registerToolFlags declares the flags of the `tool` subcommand.
func registerToolFlags(cmd *cobra.Command) {
	cmd.Flags().StringVar(&toolArgsJSON, "args", "{}", "Tool arguments as a JSON object")
}
