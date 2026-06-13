package github

import (
	"fmt"
	"os/exec"
	"strings"
)

// insteadOfRule represents a single git url.<base>.insteadOf rule.
// Git rewrites URLs that start with "value" so they start with "base" instead.
// To reverse the rewrite we replace a "base" prefix with "value".
type insteadOfRule struct {
	base  string
	value string
}

// ReverseInsteadOf queries git config for url.<base>.insteadOf rules and
// returns the original URL by undoing the rewrite. If no rule matches, the
// input URL is returned unchanged.
func ReverseInsteadOf(remoteURL string) (string, error) {
	rules, err := loadInsteadOfRules()
	if err != nil {
		return "", err
	}
	return applyReverseInsteadOf(remoteURL, rules), nil
}

// loadInsteadOfRules shells out to git config to collect all
// url.<base>.insteadOf entries from the usual config files.
func loadInsteadOfRules() ([]insteadOfRule, error) {
	cmd := exec.Command("git", "config", "--includes", "--get-regexp", `^url\..*\.insteadof$`)
	output, err := cmd.Output()
	if err != nil {
		// Exit code 1 means no matching entries were found.
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read git insteadOf rules: %w", err)
	}
	return parseInsteadOfRules(string(output)), nil
}

// parseInsteadOfRules parses the output of
// "git config --get-regexp '^url\..*\.insteadof$'".
// Each line has the form "url.<base>.insteadof <value>".
func parseInsteadOfRules(output string) []insteadOfRule {
	var rules []insteadOfRule
	for line := range strings.SplitSeq(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		key, value, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		if !strings.HasPrefix(key, "url.") || !strings.HasSuffix(key, ".insteadof") {
			continue
		}

		base := strings.TrimPrefix(key, "url.")
		base = strings.TrimSuffix(base, ".insteadof")
		rules = append(rules, insteadOfRule{base: base, value: value})
	}
	return rules
}

// applyReverseInsteadOf applies the reverse of the given insteadOf rules to
// remoteURL. The first matching rule wins.
func applyReverseInsteadOf(remoteURL string, rules []insteadOfRule) string {
	for _, rule := range rules {
		if rule.base == "" {
			continue
		}
		if rest, ok := strings.CutPrefix(remoteURL, rule.base); ok {
			return rule.value + rest
		}
	}
	return remoteURL
}
