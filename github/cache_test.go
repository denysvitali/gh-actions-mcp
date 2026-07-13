package github

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestETagCacheTransportRevalidatesWithoutServingStaleData(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write([]byte(`{"id":1}`))
	}))
	defer server.Close()

	transport := NewETagCacheTransport(nil)
	client := &http.Client{Transport: transport}
	for range 2 {
		resp, err := client.Get(server.URL)
		require.NoError(t, err)
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.Equal(t, `{"id":1}`, string(body))
	}
	require.Equal(t, int32(2), calls.Load())
	require.Equal(t, ETagCacheStats{Hits: 1, Revalidations: 1, Stores: 1}, transport.Stats())
}
