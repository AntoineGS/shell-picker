package app

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/fzf"
	"github.com/AntoineGS/shell-picker/internal/protocol"
	"github.com/AntoineGS/shell-picker/internal/sessionipc"
)

func TestInitialEnrichmentReloadEndsInitialInputBeforeLoadStarts(t *testing.T) {
	for _, event := range []protocol.Event{
		{Opcode: protocol.OpParent}, {Opcode: protocol.OpRestoreView},
		{Opcode: protocol.OpSlash, Query: []byte("missing-child")},
	} {
		t.Run(string(event.Opcode), func(t *testing.T) {
			actor := newEnrichmentActor(t, protocol.ModeInsert)
			source := &controlledInitialZoxideSource{
				started: make(chan struct{}), finished: make(chan struct{}), release: make(chan struct{}),
				result: enrichmentSource("/late"),
			}
			stream := fzf.NewInputStream(nil)
			enrichment := newTestEnrichment(t, context.Background(), actor, source, stream)
			if err := enrichment.Activate(1); err != nil {
				t.Fatal(err)
			}
			result, err := enrichment.HandleEvent(context.Background(), event)
			if err != nil {
				t.Fatal(err)
			}
			if err := enrichment.FinalizeEvent(context.Background(), sessionipc.EventFinalizeRequest{EventID: result.EventID, Applied: true}); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { _, err := io.ReadAll(stream); done <- err }()
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("initial input must end normally before reload: %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("initial input remains open; fzf cannot start reload until EOF")
			}
			if event.Opcode == protocol.OpSlash {
				if !result.Effect.InvalidPath {
					t.Fatalf("slash effect=%+v; want invalid path", result.Effect)
				}
				result, err = enrichment.HandleEvent(context.Background(), protocol.Event{Opcode: protocol.OpRestoreView})
				if err != nil {
					t.Fatal(err)
				}
			}
			if data := acknowledgeEnrichmentEvent(t, enrichment, result); len(data) == 0 {
				t.Fatal("closing initial input lost reserved generation records")
			}
		})
	}
}
