package session

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/pathutil"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func applyCreatedTransition(ctx context.Context, actor *Actor, proposal ProposedTransition, previous Snapshot) (TransitionResult, error) {
	state := &proposal.State
	if state.Completion != nil && previous.state.Browser != nil {
		query := string(previous.state.Browser.Query)
		text := pathutil.FormatCompletion(state.Completion.Base, string(state.Home.Path), query, string(state.Location.Path))
		setCompletionQuery(previous, &proposal, strings.TrimSuffix(text, "/")+"/", true)
		proposal.Effect.ClearQuery = false
	}
	result, err := actor.Apply(ctx, proposal)
	if err == nil {
		decorateCompletionEffect(&result)
	}
	return result, err
}

func reduceCompletion(snapshot Snapshot, proposal *ProposedTransition, event protocol.Event) (bool, error) {
	state := &proposal.State
	query := string(event.Query)
	if event.Opcode == protocol.OpQuery {
		if state.Source == protocol.SourceBrowser && state.Mode != protocol.ModeAdd {
			setCompletionQuery(snapshot, proposal, query, false)
		}
		return true, nil
	}
	if state.Mode == protocol.ModeAdd {
		if event.Opcode == protocol.OpEscape && state.Browser != nil {
			setMode(proposal, protocol.ModeNormal, false)
			state.Query = bytes.Clone(state.Browser.Query)
			proposal.Effect.Query, proposal.Effect.SetQuery = string(state.Query), true
			return true, nil
		}
		return false, nil
	}
	switch event.Opcode {
	case protocol.OpSlash, protocol.OpHome:
		if state.Mode == protocol.ModeInsert {
			proposal.Effect.Put = "/"
			if event.Opcode == protocol.OpHome {
				proposal.Effect.Put = "~"
			}
		} else {
			proposal.Effect.Ignore = true
		}
	case protocol.OpForward:
		if len(event.CurrentItem) == 0 {
			proposal.Effect.Ignore = true
			return true, nil
		}
		record, err := resolveNavigationRecord(snapshot, event.CurrentItem)
		if err != nil {
			return true, err
		}
		if record.Kind == protocol.KindFile {
			proposal.Effect.Ignore = true
			return true, nil
		}
		if record.Target.Kind != pathutil.KindFilesystem {
			return false, nil // retain native drive navigation
		}
		if !canNavigate(state.Picker, record.Kind) {
			return true, ErrInvalidNavigation
		}
		text := pathutil.FormatCompletion(state.Completion.Base, string(state.Home.Path), query, string(record.Path))
		if state.Source == protocol.SourceIndexed {
			if state.Indexed == nil {
				state.Indexed = &IndexedContext{}
			}
			state.Indexed.Query = bytes.Clone(event.Query)
		}
		state.Source = protocol.SourceBrowser
		setCompletionQuery(snapshot, proposal, strings.TrimSuffix(text, "/")+"/", true)
	case protocol.OpParent:
		if state.Source == protocol.SourceIndexed {
			proposal.Effect.Ignore = true
			return true, nil
		}
		parent := filepath.Dir(string(state.Location.Path))
		text := pathutil.FormatCompletion(state.Completion.Base, string(state.Home.Path), query, parent)
		setCompletionQuery(snapshot, proposal, strings.TrimSuffix(text, "/")+"/", true)
	default:
		return false, nil
	}
	return true, nil
}

func setCompletionQuery(snapshot Snapshot, proposal *ProposedTransition, query string, replace bool) {
	state := &proposal.State
	resolved := pathutil.ResolveCompletion(state.Completion.Base, string(state.Home.Path), query)
	if string(snapshot.state.Location.Path) == state.Completion.Base && snapshot.state.Source == protocol.SourceBrowser {
		state.Completion.records = snapshot.records
	}
	state.Query = []byte(query)
	if state.Browser == nil {
		state.Browser = &BrowserContext{}
	}
	state.Browser.Query = []byte(query)
	state.Browser.Mode = state.Mode
	proposal.Effect.Query, proposal.Effect.SetQuery = query, replace
	if resolved.Directory != string(snapshot.state.Location.Path) || snapshot.state.Source != protocol.SourceBrowser {
		state.Location = pathutil.Filesystem([]byte(resolved.Directory))
		state.Browser.Location = cloneLocation(state.Location)
		state.Browser.SelectedPaths = [][]byte{}
		state.Browser.records = nil
		proposal.Build = &candidate.BuildRequest{Picker: state.Picker, Location: cloneLocation(state.Location)}
		if resolved.Directory == state.Completion.Base {
			proposal.restoreRecords = state.Completion.records
		}
		proposal.Effect.ClearMulti = true
		proposal.Effect.Header = pathutil.PromptDisplayHome(state.Location, state.Home)
	}
}

func decorateCompletionEffect(result *TransitionResult) {
	state := result.Snapshot.state
	if state.Completion == nil {
		return
	}
	effect := &result.Effect
	effect.Completion = true
	effect.SetSearchQuery = true
	query := string(state.Query)
	if effect.SetQuery {
		query = effect.Query
	} else if effect.ClearQuery {
		query = ""
	}
	effect.SearchQuery = query
	if state.Source == protocol.SourceBrowser && state.Mode != protocol.ModeAdd {
		effect.SearchQuery = pathutil.ResolveCompletion(state.Completion.Base, string(state.Home.Path), query).Search
	}
}
