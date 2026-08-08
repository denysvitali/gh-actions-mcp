package mcp

import (
	"errors"
	"fmt"

	"github.com/denysvitali/gh-actions-mcp/config"
	"github.com/denysvitali/gh-actions-mcp/github"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sirupsen/logrus"
)

// MCPServer is the MCP transport layer over package github: it owns the SDK
// server, the tool and resource registrations, and one GitHub client for the
// repository configured at construction time.
//
// A value returned by NewMCPServer is ready to serve. It is safe for concurrent
// use: the SDK server handles its own sessions, and the only mutable state is
// the lazily created loopback session described in invoke.go.
type MCPServer struct {
	srv    *sdkmcp.Server
	client *github.Client
	config *config.Config
	// log is the logger this server was built with. NewMCPServer also installs
	// it as package github's logger, which is where all current logging happens;
	// it is kept here so handlers can log without reaching for a global.
	log     *logrus.Logger
	version string

	// invoke is the loopback session used by InvokeTool. See localSession for
	// its concurrency contract.
	invoke localSession
}

const (
	// DefaultListLimit is the number of items a list tool returns when neither
	// the caller nor the config asks for a size. It is deliberately small
	// (reduced from 10): every extra item costs the client context window.
	DefaultListLimit = 5
	// DefaultLogLines is how many trailing lines of log output survive
	// auto-truncation when the caller applied no limiting argument (reduced from
	// 100 for the same reason).
	DefaultLogLines = 50
)

// getLimit returns the limit from config or default
func (s *MCPServer) getLimit() int {
	if s.config.DefaultLimit > 0 {
		return s.config.DefaultLimit
	}
	return DefaultListLimit
}

// getLogLines returns the max log lines from config or default
func (s *MCPServer) getLogLines() int {
	if s.config.DefaultLogLen > 0 {
		return s.config.DefaultLogLen
	}
	return DefaultLogLines
}

// NewMCPServer builds a fully registered server for cfg. It guarantees that the
// returned server has every tool and resource registered, so the caller may
// serve it immediately; it never returns a non-nil server together with an
// error. cfg is required; a nil log is replaced by a default logrus logger.
func NewMCPServer(cfg *config.Config, log *logrus.Logger) (*MCPServer, error) {
	if cfg == nil {
		return nil, errors.New("config is required")
	}
	if log == nil {
		log = logrus.New()
	}

	serverVersion := cfg.ServerVersion
	if serverVersion == "" {
		serverVersion = "dev"
	}
	s := sdkmcp.NewServer(
		&sdkmcp.Implementation{Name: "github-actions-mcp", Version: serverVersion},
		&sdkmcp.ServerOptions{
			Instructions: "Use read-only tools to inspect GitHub Actions. Use manage_run or download_artifact only when the caller explicitly requests a mutation or local file write. Prefer get_run with element=info before fetching logs or artifacts.",
			PageSize:     25,
		},
	)

	github.SetLogger(log)

	// Use configured per-page limit or default to defaultPerPageLimit
	perPageLimit := cfg.PerPageLimit
	if perPageLimit <= 0 {
		perPageLimit = defaultPerPageLimit
	}

	ghClient, err := github.NewClientWithOptions(github.ClientOptions{
		Token:        cfg.Token,
		Owner:        cfg.RepoOwner,
		Repo:         cfg.RepoName,
		PerPageLimit: perPageLimit,
		APIBaseURL:   cfg.APIBaseURL,
		UploadURL:    cfg.UploadURL,
		RetryMax:     cfg.RetryMax,
		AuthUsername: cfg.AuthUsername,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create GitHub client: %w", err)
	}

	mcpServer := &MCPServer{
		srv:     s,
		client:  ghClient,
		config:  cfg,
		log:     log,
		version: serverVersion,
	}

	mcpServer.registerTools()
	mcpServer.registerResources()

	return mcpServer, nil
}

// registerTools declares every tool this server exposes, one file per tool
// family. The call order below is the historical registration order and is kept
// deliberately: it is the order in which tools are added to the SDK server.
func (s *MCPServer) registerTools() {
	b := toolBuilder{}
	s.registerRunTools(b)              // list_workflows, list_runs
	s.registerGetRunTool(b)            // get_run
	s.registerTimingTools(b)           // analyze_timing
	s.registerCheckStatusTools(b)      // get_check_status
	s.registerWaitTools(b)             // wait_for_run, wait_all, wait_for_commit_checks
	s.registerManageRunTool(b)         // manage_run
	s.registerArtifactReadTools(b)     // get_artifact
	s.registerDiagnosisTools(b)        // diagnose_failure
	s.registerArtifactDownloadTools(b) // download_artifact
}

// GetServer returns the underlying SDK server, already populated with this
// server's tools and resources, for wiring into a transport.
func (s *MCPServer) GetServer() *sdkmcp.Server {
	return s.srv
}
