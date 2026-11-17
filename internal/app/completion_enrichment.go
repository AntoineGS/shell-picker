package app

import (
	"context"

	"github.com/AntoineGS/shell-picker/internal/pathutil"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

// Preserve the complete launch snapshot before a completion event can leave it.
// Wait before reserving the event: publication needs the same coordinator gate.
// Local-first startup and filename-only edits remain independent of the source;
// navigation waits only for the remainder of its configured zoxide timeout.
func (enrichment *initialEnrichment) awaitCompletionLaunch(ctx context.Context, event protocol.Event) error {
	select {
	case <-enrichment.done:
		return nil
	default:
	}
	if ctx == nil {
		ctx = context.Background()
	}
	state, err := enrichment.actor.CurrentState(ctx)
	if err != nil {
		return err
	}
	if state.Completion == nil {
		return nil
	}
	switch event.Opcode {
	case protocol.OpQuery:
		if state.Source != protocol.SourceBrowser || state.Mode == protocol.ModeAdd {
			return nil
		}
		path := pathutil.ResolveCompletion(state.Completion.Base, string(state.Home.Path), string(event.Query))
		if path.Directory == string(state.Location.Path) {
			return nil
		}
	case protocol.OpForward, protocol.OpParent, protocol.OpSourceToggle, protocol.OpFilesSearch:
		// These events can replace the launch view or save it for restoration.
	case protocol.OpEnter:
		if state.Mode != protocol.ModeAdd {
			return nil
		}
	default:
		return nil
	}
	select {
	case <-enrichment.done:
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}
