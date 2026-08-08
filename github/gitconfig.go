package github

import (
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"strings"
)

// gitConfigEntry is a single key/value pair as reported by
// "git config --null --list".
type gitConfigEntry struct {
	key   string
	value string
}

// loadGitConfigEntries returns every git config entry visible from dir
// (system, global, worktree and local files, following include directives).
// dir may be empty to use the current working directory.
func loadGitConfigEntries(dir string) ([]gitConfigEntry, error) {
	//nolint:noctx // Configuration loading has no request context; Command is kept behavior-compatible here.
	cmd := exec.Command("git", "config", "--includes", "--null", "--list")
	if dir != "" {
		cmd.Dir = dir
	}
	output, err := cmd.Output()
	if err != nil {
		// Exit code 1 means "no entries", which is not an error for us.
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read git config: %w", err)
	}
	return parseGitConfigNull(string(output)), nil
}

// parseGitConfigNull parses the NUL-delimited output of
// "git config --null --list". Each record is "key\nvalue" (or just "key" for
// a valueless boolean), records separated by NUL.
func parseGitConfigNull(output string) []gitConfigEntry {
	var entries []gitConfigEntry
	for record := range strings.SplitSeq(output, "\x00") {
		if record == "" {
			continue
		}
		key, value, _ := strings.Cut(record, "\n")
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		entries = append(entries, gitConfigEntry{key: key, value: value})
	}
	return entries
}

// entriesToInsteadOfRules extracts url.<base>.insteadOf and
// url.<base>.pushInsteadOf rules from raw git config entries.
func entriesToInsteadOfRules(entries []gitConfigEntry) []insteadOfRule {
	var rules []insteadOfRule
	for _, e := range entries {
		key := e.key
		if !strings.HasPrefix(key, "url.") {
			continue
		}
		lower := strings.ToLower(key)
		var push bool
		var suffix string
		switch {
		case strings.HasSuffix(lower, ".pushinsteadof"):
			push = true
			suffix = key[len(key)-len(".pushinsteadof"):]
		case strings.HasSuffix(lower, ".insteadof"):
			suffix = key[len(key)-len(".insteadof"):]
		default:
			continue
		}
		base := strings.TrimSuffix(strings.TrimPrefix(key, "url."), suffix)
		if base == "" || e.value == "" {
			continue
		}
		rules = append(rules, insteadOfRule{base: base, value: e.value, push: push})
	}
	return rules
}

// ProxyInfo describes a git URL rewrite that points GitHub traffic at a
// reverse proxy (e.g. gh-proxy), together with the API base URL and
// credentials derived from it.
type ProxyInfo struct {
	// APIBaseURL is the derived GitHub API base URL on the proxy, with a
	// trailing slash and without credentials (e.g. "http://gh-proxy/api/").
	APIBaseURL string
	// Username / Password are the HTTP Basic credentials embedded in the
	// rewrite target, if any. Password must never be logged.
	Username string
	Password string
	// MatchedPrefix is the GitHub URL prefix that git rewrites
	// (the insteadOf value, e.g. "https://github.com/").
	MatchedPrefix string
	// RewriteTarget is the rewrite target with the password redacted.
	RewriteTarget string
	// Push reports whether the rule came from pushInsteadOf.
	Push bool
}

// String returns a redacted, log-safe description of the detected proxy.
func (p *ProxyInfo) String() string {
	if p == nil {
		return "<no proxy>"
	}
	auth := "none"
	if p.Username != "" {
		auth = "basic(" + p.Username + ")"
	}
	return fmt.Sprintf("api_base_url=%s auth=%s rewrite=%s insteadOf=%s",
		p.APIBaseURL, auth, p.RewriteTarget, p.MatchedPrefix)
}

// HasCredentials reports whether the rewrite target carried Basic credentials.
func (p *ProxyInfo) HasCredentials() bool {
	return p != nil && p.Username != "" && p.Password != ""
}

// DetectProxy inspects the git configuration visible from dir (empty means
// the current working directory) and returns the GitHub reverse proxy it
// routes github.com through, or nil when there is none.
func DetectProxy(dir string) (*ProxyInfo, error) {
	entries, err := loadGitConfigEntries(dir)
	if err != nil {
		return nil, err
	}
	return detectProxyFromRules(entriesToInsteadOfRules(entries)), nil
}

// detectProxyFromRules picks the best rewrite rule that sends github.com
// traffic to an http(s) host that is not GitHub. Fetch rules (insteadOf) win
// over push-only rules, and rules carrying credentials win over ones without,
// since the credentials are what the proxy authenticates with.
func detectProxyFromRules(rules []insteadOfRule) *ProxyInfo {
	var best *ProxyInfo
	for _, rule := range rules {
		if !isGitHubPrefix(rule.value) {
			continue
		}
		info, err := proxyFromRewriteTarget(rule.base)
		if err != nil {
			continue
		}
		info.MatchedPrefix = rule.value
		info.Push = rule.push
		if best == nil || proxyRank(info) > proxyRank(best) {
			best = info
		}
	}
	return best
}

// proxyRank scores a candidate so the most useful rule wins: fetch rules beat
// push rules, and credentialed rules beat anonymous ones.
func proxyRank(p *ProxyInfo) int {
	rank := 0
	if !p.Push {
		rank += 2
	}
	if p.HasCredentials() {
		rank++
	}
	return rank
}

// proxyFromRewriteTarget converts a rewrite target such as
// "http://user:pass@gh-proxy.local/git/" into API base URL + credentials.
// gh-proxy serves git over "<root>/git/" and the REST API over "<root>/api/".
func proxyFromRewriteTarget(target string) (*ProxyInfo, error) {
	u, err := url.Parse(target)
	if err != nil {
		return nil, fmt.Errorf("parse rewrite target: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("unsupported rewrite target scheme %q", u.Scheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("rewrite target has no host")
	}
	if isGitHubHost(u.Hostname()) {
		return nil, fmt.Errorf("rewrite target is GitHub itself")
	}

	info := &ProxyInfo{RewriteTarget: RedactURL(target)}
	if u.User != nil {
		info.Username = u.User.Username()
		info.Password, _ = u.User.Password()
	}

	path := strings.Trim(u.Path, "/")
	segments := []string{}
	if path != "" {
		segments = strings.Split(path, "/")
	}
	// Drop the git-specific suffix so we can swap in the API mount point.
	if n := len(segments); n > 0 && strings.EqualFold(segments[n-1], "git") {
		segments = segments[:n-1]
	}
	segments = append(segments, "api")

	apiURL := &url.URL{Scheme: u.Scheme, Host: u.Host, Path: "/" + strings.Join(segments, "/") + "/"}
	info.APIBaseURL = apiURL.String()
	return info, nil
}

// isGitHubPrefix reports whether a git insteadOf value refers to github.com,
// covering https, http, git://, ssh:// and scp-like (git@github.com:) forms.
func isGitHubPrefix(value string) bool {
	v := strings.TrimSpace(value)
	if v == "" {
		return false
	}
	if strings.Contains(v, "://") {
		u, err := url.Parse(v)
		if err != nil {
			return false
		}
		return isGitHubHost(u.Hostname())
	}
	// scp-like syntax: [user@]host:path
	hostPart, _, ok := strings.Cut(v, ":")
	if !ok {
		return false
	}
	if _, host, found := strings.Cut(hostPart, "@"); found {
		hostPart = host
	}
	return isGitHubHost(hostPart)
}

// isGitHubHost reports whether host is github.com or a subdomain of it.
func isGitHubHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	return host == "github.com" || strings.HasSuffix(host, ".github.com")
}

// RedactURL removes secrets from a URL so it is safe to log: the password in
// the userinfo section is replaced with "***" and the username is kept.
// Inputs that are not URLs are returned unchanged.
func RedactURL(raw string) string {
	if raw == "" || !strings.Contains(raw, "@") {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	if _, hasPassword := u.User.Password(); !hasPassword {
		return raw
	}
	u.User = url.UserPassword(u.User.Username(), "***")
	// url.String() percent-encodes the placeholder; keep it readable.
	return strings.Replace(u.String(), "%2A%2A%2A", "***", 1)
}
