package mcp

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/denysvitali/gh-actions-mcp/config"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPageCursorRoundTrip(t *testing.T) {
	scope := struct{ Repo string }{"owner/repo"}
	key := []byte("test-key")
	cursor, err := encodeCursor(cursorPosition{Page: 42, Offset: 3}, scope, key)
	require.NoError(t, err)
	assert.NotEmpty(t, cursor)
	position, err := decodeCursor(cursor, scope, key)
	require.NoError(t, err)
	assert.Equal(t, cursorPosition{Page: 42, Offset: 3}, position)
}

func TestListRunsFillsFilteredPageAcrossGitHubPages(t *testing.T) {
	var serverURL string
	var mu sync.Mutex
	requestedPages := []string{}
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/repo/actions/runs", func(w http.ResponseWriter, request *http.Request) {
		page := request.URL.Query().Get("page")
		if page == "" {
			page = "1"
		}
		mu.Lock()
		requestedPages = append(requestedPages, page)
		mu.Unlock()
		if page != "3" {
			next := "2"
			if page == "2" {
				next = "3"
			}
			w.Header().Set("Link", fmt.Sprintf(`<%s/repos/owner/repo/actions/runs?page=%s>; rel="next"`, serverURL, next))
		}
		conclusions := map[string][]string{"1": {"success", "success"}, "2": {"failure", "success"}, "3": {"failure"}}
		body := `{"total_count":5,"workflow_runs":[`
		for index, conclusion := range conclusions[page] {
			if index > 0 {
				body += ","
			}
			body += fmt.Sprintf(`{"id":%d,"name":"CI","status":"completed","conclusion":%q}`, 100+len(requestedPages)*10+index, conclusion)
		}
		body += `]}`
		_, _ = w.Write([]byte(body))
	})
	httpServer := httptest.NewServer(mux)
	defer httpServer.Close()
	serverURL = httpServer.URL
	server, err := NewMCPServer(&config.Config{Token: "token", RepoOwner: "owner", RepoName: "repo", APIBaseURL: serverURL + "/", UploadURL: serverURL + "/"}, logrus.New())
	require.NoError(t, err)

	_, output, err := server.listRunsTyped(context.Background(), nil, listRunsInput{Conclusion: "failure", PerPage: 2})
	require.NoError(t, err)
	require.Len(t, output.Runs, 2)
	assert.Empty(t, output.NextCursor)
	assert.Equal(t, []string{"1", "2", "3"}, requestedPages)
}

func TestListWorkflowsCursorPagination(t *testing.T) {
	var serverURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/repo/actions/workflows", func(w http.ResponseWriter, request *http.Request) {
		page := request.URL.Query().Get("page")
		if page == "" || page == "1" {
			w.Header().Set("Link", fmt.Sprintf(`<%s/repos/owner/repo/actions/workflows?page=2>; rel="next"`, serverURL))
			_, _ = w.Write([]byte(`{"total_count":2,"workflows":[{"id":1,"name":"CI","path":"ci.yml","state":"active"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"total_count":2,"workflows":[{"id":2,"name":"Release","path":"release.yml","state":"active"}]}`))
	})
	httpServer := httptest.NewServer(mux)
	defer httpServer.Close()
	serverURL = httpServer.URL

	server, err := NewMCPServer(&config.Config{
		Token: "token", RepoOwner: "owner", RepoName: "repo",
		APIBaseURL: httpServer.URL + "/", UploadURL: httpServer.URL + "/",
	}, logrus.New())
	require.NoError(t, err)

	_, firstPage, err := server.listWorkflowsTyped(context.Background(), nil, listWorkflowsInput{Limit: 1})
	require.NoError(t, err)
	require.Len(t, firstPage.Workflows, 1)
	assert.Equal(t, "CI", firstPage.Workflows[0].Name)
	assert.NotEmpty(t, firstPage.NextCursor)

	_, secondPage, err := server.listWorkflowsTyped(context.Background(), nil, listWorkflowsInput{Limit: 1, Cursor: firstPage.NextCursor})
	require.NoError(t, err)
	require.Len(t, secondPage.Workflows, 1)
	assert.Equal(t, "Release", secondPage.Workflows[0].Name)
	assert.Empty(t, secondPage.NextCursor)
}

func TestPageCursorDefaultsAndRejectsInvalidValues(t *testing.T) {
	scope := struct{ Repo string }{"owner/repo"}
	key := []byte("test-key")
	position, err := decodeCursor("", scope, key)
	require.NoError(t, err)
	assert.Equal(t, cursorPosition{Page: 1}, position)

	_, err = decodeCursor("not-a-cursor", scope, key)
	assert.EqualError(t, err, "invalid cursor")

	cursor, err := encodeCursor(cursorPosition{Page: 2}, scope, key)
	require.NoError(t, err)
	_, err = decodeCursor(cursor, struct{ Repo string }{"other/repo"}, key)
	assert.EqualError(t, err, "cursor does not match the current repository or filters")

	_, err = decodeCursor(cursor, scope, []byte("other-key"))
	assert.EqualError(t, err, "invalid cursor signature")
}
