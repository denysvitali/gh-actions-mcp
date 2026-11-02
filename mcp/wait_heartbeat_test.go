package mcp

import (
	"context"
	"sync"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordingNotifier struct {
	mu    sync.Mutex
	beats []*sdkmcp.ProgressNotificationParams
}

func (r *recordingNotifier) NotifyProgress(_ context.Context, params *sdkmcp.ProgressNotificationParams) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.beats = append(r.beats, params)
	return nil
}

func (r *recordingNotifier) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.beats)
}

func TestWaitHeartbeat_SendsProgressUntilStopped(t *testing.T) {
	notifier := &recordingNotifier{}
	stop := heartbeat(context.Background(), notifier, "tok", "run 7", 5*time.Millisecond)
	require.Eventually(t, func() bool { return notifier.count() >= 2 }, time.Second, time.Millisecond)
	stop()
	after := notifier.count()
	time.Sleep(20 * time.Millisecond)
	assert.Equal(t, after, notifier.count(), "no progress may follow stop")
	assert.Equal(t, "tok", notifier.beats[0].ProgressToken)
	assert.Contains(t, notifier.beats[0].Message, "run 7: still waiting")
	assert.Less(t, notifier.beats[0].Progress, notifier.beats[1].Progress)
}

func TestWaitHeartbeat_WithoutTokenIsNoop(t *testing.T) {
	startWaitHeartbeat(context.Background(), nil, "x")()
	startWaitHeartbeat(context.Background(), &sdkmcp.CallToolRequest{Params: &sdkmcp.CallToolParamsRaw{}}, "x")()
}
