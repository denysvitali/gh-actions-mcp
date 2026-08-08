package github

import (
	"fmt"
	"os"
	"strings"

	"github.com/go-git/go-git/v5"
)

// GetCurrentBranch attempts to detect the current git branch from the working directory.
// Returns empty string if not in a git repository, in detached HEAD state, or on error.
func GetCurrentBranch() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("failed to get working directory: %w", err)
	}

	repo, err := git.PlainOpen(wd)
	if err != nil {
		return "", fmt.Errorf("not in a git repository: %w", err)
	}

	head, err := repo.Head()
	if err != nil {
		return "", fmt.Errorf("failed to get HEAD: %w", err)
	}

	if !head.Name().IsBranch() {
		log.Warnf("HEAD is detached (not on a branch)")
		return "", nil
	}

	return head.Name().Short(), nil
}

// CommitInfo contains information about a git commit
type CommitInfo struct {
	SHA    string `json:"sha"`
	Author string `json:"author"`
	Date   string `json:"date"`
	Msg    string `json:"message"`
}

// GetLastCommit returns information about the current HEAD commit.
// Returns nil if not in a git repository or on error.
func GetLastCommit() (*CommitInfo, error) {
	wd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("failed to get working directory: %w", err)
	}

	repo, err := git.PlainOpen(wd)
	if err != nil {
		return nil, fmt.Errorf("not in a git repository: %w", err)
	}

	head, err := repo.Head()
	if err != nil {
		return nil, fmt.Errorf("failed to get HEAD: %w", err)
	}

	commit, err := repo.CommitObject(head.Hash())
	if err != nil {
		return nil, fmt.Errorf("failed to get commit object: %w", err)
	}

	return &CommitInfo{
		SHA:    head.Hash().String()[:7],
		Author: commit.Author.Name,
		Date:   commit.Author.When.Format("2006-01-02 15:04:05"),
		Msg:    strings.SplitN(commit.Message, "\n", 2)[0], // First line only
	}, nil
}
