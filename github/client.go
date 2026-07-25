package github

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/go-github/v89/github"
	"github.com/sirupsen/logrus"
)

// log is the package-wide logger. It is a mutable global by design: the CLI
// installs its logger once, during start-up, before any client or detector is
// used. Writing it after goroutines have started is a data race.
var log = logrus.New()

// SetLogger installs the logger used by every function in this package,
// including the repository detector. Call it once during start-up, before any
// concurrent use: the write is unsynchronised.
func SetLogger(l *logrus.Logger) {
	log = l
}

// SetDetectorLogger is an alias for SetLogger, kept because callers configure
// the detector separately. Both setters write the same package logger, so the
// last call wins.
func SetDetectorLogger(l *logrus.Logger) {
	log = l
}

// maxRedirects is the maximum number of HTTP redirects to follow
const maxRedirects = 10

// presignedHTTPClient is used for fetching pre-signed storage URLs (no auth headers)
var presignedHTTPClient = &http.Client{Timeout: 30 * time.Second}

type Client struct {
	owner        string
	repo         string
	gh           *github.Client
	perPageLimit int
	retry        *RetryTransport
	cache        *ETagCacheTransport
}

func NewClient(token, owner, repo string) *Client {
	return NewClientWithPerPage(token, owner, repo, 50)
}

// NewClientWithPerPage creates a new GitHub client with a custom per-page limit
func NewClientWithPerPage(token, owner, repo string, perPageLimit int) *Client {
	c, _ := NewClientWithOptions(ClientOptions{
		Token:        token,
		Owner:        owner,
		Repo:         repo,
		PerPageLimit: perPageLimit,
	})
	return c
}

// ClientOptions configures a GitHub client. APIBaseURL / UploadURL enable
// routing through a GitHub Enterprise server or a reverse proxy like gh-proxy.
type ClientOptions struct {
	Token        string
	Owner        string
	Repo         string
	PerPageLimit int
	// APIBaseURL overrides the default https://api.github.com/ base URL.
	// Must end with a trailing slash (go-github requirement). Example for
	// gh-proxy: "http://gh-proxy:8080/api/".
	APIBaseURL string
	// UploadURL overrides the upload URL. Defaults to APIBaseURL when empty.
	UploadURL string
	// RetryMax controls retries for safe read requests when GitHub responds with
	// transient or rate-limit errors. Zero uses the default; a negative value
	// disables retries.
	RetryMax int
	// AuthUsername, when non-empty, switches authentication from a Bearer
	// token to HTTP Basic auth (username:token). This is required by some
	// reverse proxies (e.g. gh-proxy) that do not accept Bearer tokens.
	AuthUsername string
}

// NewClientWithOptions creates a new GitHub client using the provided options.
func NewClientWithOptions(opts ClientOptions) (*Client, error) {
	if opts.PerPageLimit <= 0 {
		opts.PerPageLimit = 50
	}
	retryMax := opts.RetryMax
	if retryMax == 0 {
		retryMax = DefaultRetryMax
	}
	retry := NewRetryTransport(nil, retryMax)
	cache := NewETagCacheTransport(retry)
	var hc *http.Client
	var clientOpts []github.ClientOptionsFunc
	if opts.AuthUsername != "" {
		// HTTP Basic auth (required by some reverse proxies like gh-proxy).
		// Credentials live in the transport, not as a bearer token.
		basic := &github.BasicAuthTransport{
			Username:  opts.AuthUsername,
			Password:  opts.Token,
			Transport: cache,
		}
		hc = basic.Client()
	} else {
		hc = &http.Client{
			Timeout:   30 * time.Second,
			Transport: cache,
		}
		if opts.Token != "" {
			clientOpts = append(clientOpts, github.WithAuthToken(opts.Token))
		}
	}
	clientOpts = append(clientOpts, github.WithHTTPClient(hc))
	if opts.APIBaseURL != "" {
		// Use WithURLs rather than WithEnterpriseURLs; the latter auto-
		// appends "api/v3/" and breaks non-Enterprise proxies (e.g. gh-proxy,
		// which expects "/api/repos/...").
		base, err := url.Parse(opts.APIBaseURL)
		if err != nil {
			return nil, fmt.Errorf("parse api_base_url: %w", err)
		}
		if !strings.HasSuffix(base.Path, "/") {
			base.Path += "/"
		}
		uploadStr := opts.UploadURL
		if uploadStr == "" {
			uploadStr = opts.APIBaseURL
		}
		upload, err := url.Parse(uploadStr)
		if err != nil {
			return nil, fmt.Errorf("parse upload_url: %w", err)
		}
		if !strings.HasSuffix(upload.Path, "/") {
			upload.Path += "/"
		}
		baseURL, uploadURL := base.String(), upload.String()
		clientOpts = append(clientOpts, github.WithURLs(&baseURL, &uploadURL))
	}
	gh, err := github.NewClient(clientOpts...)
	if err != nil {
		return nil, fmt.Errorf("create GitHub client: %w", err)
	}
	return &Client{
		owner:        opts.Owner,
		repo:         opts.Repo,
		gh:           gh,
		perPageLimit: opts.PerPageLimit,
		retry:        retry,
		cache:        cache,
	}, nil
}

type ClientTransportStats struct {
	Retry RetryStatsSnapshot
	Cache ETagCacheStats
}

func (c *Client) TransportStats() ClientTransportStats {
	return ClientTransportStats{Retry: c.retry.Stats(), Cache: c.cache.Stats()}
}

func (c *Client) GetRepoInfo() (string, string) {
	return c.owner, c.repo
}
