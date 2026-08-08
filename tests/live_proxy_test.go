//go:build live

package tests

import (
	"context"
	"testing"
	"time"

	"github.com/denysvitali/gh-actions-mcp/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Live tests exercise the real gh-proxy the machine's git config points at.
// They only run with "-tags live" and never log credentials: the proxy
// password stays inside ProxyInfo and is only used as the Basic-auth password.
//
// Run with: go test -tags live ./tests/ -run Live -v

const (
	liveProxyOwner = "denysvitali"
	liveProxyRepo  = "gh-actions-mcp"
)

// newLiveProxyClient derives the API base URL and credentials from git's
// url.<proxy>.insteadOf rules, exactly like the CLI's proxy detection.
func newLiveProxyClient(t *testing.T) *github.Client {
	t.Helper()

	proxy, err := github.DetectProxy("")
	require.NoError(t, err)
	require.NotNil(t, proxy, "no gh-proxy rewrite found in git config; run on a machine that uses gh-proxy")
	require.True(t, proxy.HasCredentials(), "gh-proxy rewrite carries no credentials")

	// Redacted form only: this is the only proxy value that may appear in
	// test output.
	t.Logf("using proxy %s", proxy)

	client, err := github.NewClientWithOptions(github.ClientOptions{
		Token:        proxy.Password,
		Owner:        liveProxyOwner,
		Repo:         liveProxyRepo,
		APIBaseURL:   proxy.APIBaseURL,
		AuthUsername: proxy.Username,
	})
	require.NoError(t, err)
	return client
}

func TestLiveProxy_ListWorkflows(t *testing.T) {
	client := newLiveProxyClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	workflows, err := client.GetWorkflows(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, workflows, "gh-actions-mcp should have at least one workflow")

	for _, wf := range workflows {
		assert.NotEmpty(t, wf.Name)
		t.Logf("workflow %d: %s (%s)", wf.ID, wf.Name, wf.State)
	}
}

func TestLiveProxy_ListRepositoryWorkflowRuns(t *testing.T) {
	client := newLiveProxyClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	runs, err := client.ListRepositoryWorkflowRunsWithOptions(ctx, &github.ListRunsOptions{Per_page: 5})
	require.NoError(t, err)
	require.NotEmpty(t, runs, "gh-actions-mcp should have workflow runs")

	run := runs[0]
	assert.NotZero(t, run.ID)
	assert.NotEmpty(t, run.Status)
	t.Logf("latest run: %d %s -> %s/%s", run.ID, run.Name, run.Status, run.Conclusion)
}

func TestLiveProxy_GetWorkflowRunAndJobs(t *testing.T) {
	client := newLiveProxyClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	runs, err := client.ListRepositoryWorkflowRunsWithOptions(ctx, &github.ListRunsOptions{Per_page: 1})
	require.NoError(t, err)
	require.NotEmpty(t, runs)

	run, err := client.GetWorkflowRun(ctx, runs[0].ID)
	require.NoError(t, err)
	assert.Equal(t, runs[0].ID, run.ID)

	jobs, err := client.GetWorkflowJobs(ctx, run.ID, "", 0)
	require.NoError(t, err)
	require.NotEmpty(t, jobs, "a workflow run should have jobs")
	t.Logf("run %d has %d jobs, first: %s (%s)", run.ID, len(jobs), jobs[0].Name, jobs[0].Status)
}

func TestLiveProxy_CheckStatusForRef(t *testing.T) {
	client := newLiveProxyClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	runs, err := client.ListRepositoryWorkflowRunsWithOptions(ctx, &github.ListRunsOptions{Per_page: 1})
	require.NoError(t, err)
	require.NotEmpty(t, runs)
	require.NotEmpty(t, runs[0].HeadSHA)

	// The client answers check-status queries from workflow runs because
	// gh-proxy does not expose the Checks API; exercise that path.
	status, err := client.GetCheckRunsForRef(ctx, runs[0].HeadSHA, nil)
	require.NoError(t, err)
	require.NotNil(t, status)
	t.Logf("ref %.7s: %d check entries", runs[0].HeadSHA, status.TotalCount)
}
