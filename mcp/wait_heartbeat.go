package mcp

import (
	"context"
	"fmt"
	"sync"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// waitHeartbeatInterval spaces progress notifications during a wait. MCP
// clients such as Claude Code abort a call that sends neither a response nor
// progress for 30 minutes, which is exactly as long as a default wait.
var waitHeartbeatInterval = 30 * time.Second

// progressNotifier is the part of a server session a wait heartbeat uses.
type progressNotifier interface {
	NotifyProgress(context.Context, *sdkmcp.ProgressNotificationParams) error
}

// startWaitHeartbeat reports progress while a wait tool polls, when the caller
// supplied a progress token. The returned stop function blocks until the
// heartbeat has exited, so no notification follows the tool result.
func startWaitHeartbeat(ctx context.Context, request *sdkmcp.CallToolRequest, label string) func() {
	if request == nil || request.Session == nil || request.Params == nil {
		return func() {}
	}
	token := request.Params.GetProgressToken()
	if token == nil {
		return func() {}
	}
	return heartbeat(ctx, request.Session, token, label, waitHeartbeatInterval)
}

func heartbeat(ctx context.Context, notifier progressNotifier, token any, label string, interval time.Duration) func() {
	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		start := time.Now()
		for beats := 1; ; beats++ {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = notifier.NotifyProgress(ctx, &sdkmcp.ProgressNotificationParams{
					ProgressToken: token,
					Progress:      float64(beats),
					Message:       fmt.Sprintf("%s: still waiting after %s", label, time.Since(start).Round(time.Second)),
				})
			}
		}
	}()
	return func() {
		cancel()
		wg.Wait()
	}
}
