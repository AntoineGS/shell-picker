package session

import (
	"bytes"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

// FilesSearch identifies an explicitly files-only indexed search.
func (state State) FilesSearch() bool {
	return state.Source == protocol.SourceIndexed && state.Indexed != nil && state.Indexed.FilesOnly
}

// ActivePicker keeps the browser's command context while searching for files.
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
	if !state.Indexed.FilesOnly {
		// Directory marks must not survive narrowing the indexed candidate set.
		selected := make(map[string]bool, len(state.Indexed.SelectedPaths))
		for _, path := range state.Indexed.SelectedPaths {
			selected[string(path)] = true
		}
		state.Indexed.SelectedPaths = [][]byte{}
		if snapshot.state.Source == protocol.SourceIndexed {
			for _, record := range snapshot.recordValues() {
				if record.Kind == protocol.KindFile && selected[string(record.Path)] {
					state.Indexed.SelectedPaths = append(state.Indexed.SelectedPaths, bytes.Clone(record.Path))
				}
			}
		}
	}
	state.Indexed.FilesOnly = true
	state.Indexed.Query = bytes.Clone(state.Query)
	state.Mode = protocol.ModeInsert
	state.Prompt = modePrompt(state.Mode, false)
	state.AddError = false
	proposal.Build = &candidate.BuildRequest{
		Picker: protocol.PickerSearch, Source: protocol.SourceIndexed, Location: cloneLocation(state.Location), FilesOnly: true,
	}
	proposal.Effect = protocol.Effect{
		Mode: state.Mode, Prompt: state.Prompt, Search: "on", Rebind: state.Mode,
		Query: string(state.Query), SetQuery: true, ClearMulti: true, Cursor: protocol.CursorLine,
		Header: indexedHeader(protocol.PickerSearch, true),
	}
	return nil
}
