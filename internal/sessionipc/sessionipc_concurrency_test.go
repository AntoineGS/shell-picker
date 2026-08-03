package sessionipc

import (
	"context"
	"testing"
)

func TestServerRejectsSeventeenthHandlerAndCloseCancelsAndJoins(t *testing.T) {
	entered := make(chan struct{}, 16)
	release := make(chan struct{})
	exited := make(chan struct{}, 16)
	finished := make(chan struct{}, 16)
	requestCtx, cancelRequests := context.WithCancel(context.Background())
	defer cancelRequests()
	backend := benignBackend()
	backend.currentHeader = func(ctx context.Context) (string, error) {
		entered <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
		}
		exited <- struct{}{}
		return "", context.Cause(ctx)
	}
	server, client := startServer(t, backend)
	for range 16 {
		go func() {
			_, _ = client.Display(requestCtx)
			finished <- struct{}{}
		}()
	}
	for range 16 {
		awaitSessionIPC(t, entered, "display backend entry under handler pressure")
	}
	assertDisplayError(t, client, ErrTooManyRequests, "seventeenth display concurrency-limit request")

	closeSessionIPC(t, server, "handler cancellation and join", nil)
	assertSessionIPCListenerClosed(t, server.Address())
	for range 16 {
		awaitSessionIPC(t, exited, "display backend exit after server close")
	}
	for range 16 {
		awaitSessionIPC(t, finished, "display request completion after server close")
	}
	close(release)
	closeSessionIPC(t, server, "idempotent close", nil)
}
