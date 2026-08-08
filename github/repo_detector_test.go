package github

import (
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRepoDetector_CacheIsCopiedNotAliased(t *testing.T) {
	repoDir := newTestRepo(t, "https://github.com/owner/repo.git")
	restore := chdir(t, repoDir)
	defer restore()

	detector := NewRepoDetector()

	first, err := detector.Detect()
	require.NoError(t, err)
	assert.False(t, first.Cached)

	second, err := detector.Detect()
	require.NoError(t, err)
	assert.True(t, second.Cached)
	assert.NotSame(t, first, second, "each call returns its own RepoInfo")

	// Mutating a returned value must not corrupt the cache.
	second.Owner = "tampered"
	third, err := detector.Detect()
	require.NoError(t, err)
	assert.Equal(t, "owner", third.Owner)
}

func TestRepoDetector_ConcurrentDetectIsRaceFree(t *testing.T) {
	repoDir := newTestRepo(t, "https://github.com/owner/repo.git")
	restore := chdir(t, repoDir)
	defer restore()

	detector := NewRepoDetector()

	const goroutines = 16
	var wg sync.WaitGroup
	results := make([]*RepoInfo, goroutines)
	errs := make([]error, goroutines)
	for i := range goroutines {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%4 == 3 {
				detector.ClearCache()
			}
			results[i], errs[i] = detector.Detect()
		}(i)
	}
	wg.Wait()

	for i := range goroutines {
		require.NoError(t, errs[i])
		require.NotNil(t, results[i])
		assert.Equal(t, "owner", results[i].Owner)
		assert.Equal(t, "repo", results[i].Repo)
	}
}

func TestDetectRepoInfo(t *testing.T) {
	repoDir := newTestRepo(t, "git@github.com:owner/repo.git")
	restore := chdir(t, repoDir)
	defer restore()

	info, err := DetectRepoInfo()
	require.NoError(t, err)
	assert.Equal(t, "owner", info.Owner)
	assert.Equal(t, "repo", info.Repo)
	assert.False(t, info.Cached, "a fresh detector never reports a cache hit")
	assert.Equal(t, "git_remote(origin)", info.Source)
}

// chdir switches the process working directory and returns a restore function.
// Tests that use it must not run in parallel: the working directory is global.
func chdir(t *testing.T, dir string) func() {
	t.Helper()
	original, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	return func() {
		require.NoError(t, os.Chdir(original))
	}
}
