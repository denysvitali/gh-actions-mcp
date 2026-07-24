package github

import (
	"strings"
)

// insteadOfRule represents a single git url.<base>.insteadOf rule.
// Git rewrites URLs that start with "value" so they start with "base" instead.
// To reverse the rewrite we replace a "base" prefix with "value".
type insteadOfRule struct {
	base  string
	value string
	// push marks rules coming from pushInsteadOf rather than insteadOf.
	push bool
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

// loadInsteadOfRules reads all url.<base>.insteadOf and
// url.<base>.pushInsteadOf entries from the git config files that apply to
// the current working directory.
func loadInsteadOfRules() ([]insteadOfRule, error) {
	entries, err := loadGitConfigEntries("")
	if err != nil {
		return nil, err
	}
	return entriesToInsteadOfRules(entries), nil
}

// applyReverseInsteadOf applies the reverse of the given insteadOf rules to
// remoteURL. Mirroring git's forward behaviour, the longest matching rewrite
// target wins; ties are broken in favour of fetch rules over pushInsteadOf.
func applyReverseInsteadOf(remoteURL string, rules []insteadOfRule) string {
	var match *insteadOfRule
	for i := range rules {
		rule := &rules[i]
		if rule.base == "" || !strings.HasPrefix(remoteURL, rule.base) {
			continue
		}
		if match == nil ||
			len(rule.base) > len(match.base) ||
			(len(rule.base) == len(match.base) && match.push && !rule.push) {
			match = rule
		}
	}
	if match == nil {
		return remoteURL
	}
	return match.value + strings.TrimPrefix(remoteURL, match.base)
}

// applyInsteadOf performs git's forward rewrite: the longest matching
// insteadOf value is replaced by its rewrite target. pushInsteadOf rules are
// only considered when forPush is true.
func applyInsteadOf(remoteURL string, rules []insteadOfRule, forPush bool) string {
	var match *insteadOfRule
	for i := range rules {
		rule := &rules[i]
		if rule.value == "" || !strings.HasPrefix(remoteURL, rule.value) {
			continue
		}
		if rule.push && !forPush {
			continue
		}
		// git prefers pushInsteadOf over insteadOf when pushing.
		if match == nil ||
			len(rule.value) > len(match.value) ||
			(len(rule.value) == len(match.value) && forPush && rule.push && !match.push) {
			match = rule
		}
	}
	if match == nil {
		return remoteURL
	}
	return match.base + strings.TrimPrefix(remoteURL, match.value)
}
