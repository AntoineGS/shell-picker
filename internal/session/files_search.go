package session

import (
	"bytes"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

// FilesSearch identifies an explicitly unrestricted indexed search.
func (state State) FilesSearch() bool {
	return state.Source == protocol.SourceIndexed && state.Indexed != nil && state.Indexed.AllPaths
}

// ActivePicker keeps the browser's command context while widening file search.
func (state State) ActivePicker() protocol.Picker {
	if state.FilesSearch() {
		return protocol.PickerSearch
	}
	return state.Picker
}

func forceFilesSearch(snapshot Snapshot, proposal *ProposedTransition, event protocol.Event) error {
	state := &proposal.State
	if state.Source == protocol.SourceBrowser {
		if err := toggleSource(snapshot, proposal, event); err != nil {
			return err
		}
	} else {
		state.Query = bytes.Clone(event.Query)
	}
	if state.Indexed == nil {
		state.Indexed = &IndexedContext{SelectedPaths: [][]byte{}}
	}
	state.Indexed.AllPaths = true
	state.Indexed.Query = bytes.Clone(state.Query)
	state.Mode = protocol.ModeInsert
	state.Prompt = modePrompt(state.Mode, false)
	state.AddError = false
	proposal.Build = &candidate.BuildRequest{
		Picker: protocol.PickerSearch, Source: protocol.SourceIndexed, Location: cloneLocation(state.Location),
	}
	proposal.Effect = protocol.Effect{
		Mode: state.Mode, Prompt: state.Prompt, Search: "on", Rebind: state.Mode,
		Query: string(state.Query), SetQuery: true, ClearMulti: true, Cursor: protocol.CursorLine,
		Header: indexedHeader(protocol.PickerSearch),
	}
	return nil
}
