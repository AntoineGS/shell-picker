package session

import (
	"bytes"
	"strconv"
	"strings"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/pathutil"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

// Keep source restoration payloads bounded: even with a 256 MiB candidate
// frame, record positions remain below 2^26; 4096 marks serialize to less
// than 40 KiB.
const maxCachedSelections = 4_096

func toggleSource(snapshot Snapshot, proposal *ProposedTransition, event protocol.Event) error {
	state := &proposal.State
	if state.Source == protocol.SourceBrowser {
		fromAdd := state.Mode == protocol.ModeAdd
		query := bytes.Clone(event.Query)
		if fromAdd {
			query = nil
		}
		selected := [][]byte{}
		if state.Browser != nil && state.Browser.SelectedPaths != nil {
			selected = clonePaths(state.Browser.SelectedPaths)
		}
		state.Browser = &BrowserContext{
			Location:      cloneLocation(state.Location),
			Mode:          state.Mode,
			Query:         bytes.Clone(event.Query),
			SelectedPaths: selected,
			records:       snapshot.records,
		}
		state.Source = protocol.SourceIndexed
		state.Mode = protocol.ModeInsert
		if state.Indexed == nil {
			state.Indexed = &IndexedContext{Query: bytes.Clone(query), SelectedPaths: [][]byte{}}
		} else if !fromAdd {
			query = bytes.Clone(state.Indexed.Query)
		}
		state.Query = query
		state.AddError = false
		state.Prompt = modePrompt(state.Mode, false)
		proposal.Build = &candidate.BuildRequest{
			Picker: state.ActivePicker(), Source: protocol.SourceIndexed, Location: cloneLocation(state.Location), FilesOnly: state.FilesSearch(),
		}
		proposal.Effect = protocol.Effect{
			Mode: state.Mode, Prompt: state.Prompt, Search: "on", Rebind: state.Mode,
			Query: string(query), SetQuery: true, ClearMulti: true, Cursor: protocol.CursorLine,
			Header: indexedHeader(state.ActivePicker(), state.FilesSearch()),
		}
		return nil
	}

	if state.Browser == nil {
		proposal.Effect.Ignore = true
		return nil
	}
	if state.Indexed == nil {
		state.Indexed = &IndexedContext{SelectedPaths: [][]byte{}}
	} else if state.Indexed.SelectedPaths == nil {
		state.Indexed.SelectedPaths = [][]byte{}
	}
	state.Indexed.Query = bytes.Clone(event.Query)
	browser := *state.Browser
	state.Source = protocol.SourceBrowser
	state.Location = cloneLocation(browser.Location)
	state.Mode = browser.Mode
	state.Query = bytes.Clone(browser.Query)
	state.AddError = false
	state.Prompt = modePrompt(state.Mode, false)
	proposal.Build = &candidate.BuildRequest{
		Picker: state.Picker, Source: protocol.SourceBrowser, Location: cloneLocation(state.Location),
	}
	proposal.restoreRecords = browser.records
	proposal.Effect = protocol.Effect{
		Mode: state.Mode, Prompt: state.Prompt, Search: "on", Rebind: state.Mode,
		Query: string(state.Query), SetQuery: true, ClearMulti: true, Cursor: protocol.CursorLine,
		Header: pathutil.PromptDisplayHome(state.Location, state.Home),
	}
	if state.Mode == protocol.ModeNormal {
		proposal.Effect.Search = "off"
		proposal.Effect.Cursor = protocol.CursorBlock
	}
	return nil
}

func toggleMarkedPath(snapshot Snapshot, proposal *ProposedTransition, event protocol.Event) error {
	if len(event.CurrentItem) == 0 {
		return ErrUnknownRecord
	}
	record, err := resolveRecord(snapshot, event.CurrentItem)
	if err != nil {
		return err
	}
	if record.Target.Kind != pathutil.KindFilesystem {
		return ErrUnknownRecord
	}
	state := &proposal.State
	state.Query = bytes.Clone(event.Query)
	var selected *[][]byte
	if state.Source == protocol.SourceIndexed {
		if state.Indexed == nil {
			state.Indexed = &IndexedContext{}
		}
		state.Indexed.Query = bytes.Clone(event.Query)
		selected = &state.Indexed.SelectedPaths
	} else {
		if state.Browser == nil {
			state.Browser = &BrowserContext{Location: cloneLocation(state.Location), Mode: state.Mode}
		}
		state.Browser.Query = bytes.Clone(event.Query)
		selected = &state.Browser.SelectedPaths
	}
	for index, path := range *selected {
		if bytes.Equal(path, record.Path) {
			*selected = append((*selected)[:index], (*selected)[index+1:]...)
			proposal.Effect.ToggleSelection = true
			return nil
		}
	}
	if state.ActivePicker() == protocol.PickerCD {
		*selected = [][]byte{bytes.Clone(record.Path)}
		proposal.Effect.ClearMulti = true
		proposal.Effect.ToggleSelection = true
		return nil
	}
	if len(*selected) >= maxCachedSelections {
		return ErrTooManySelections
	}
	*selected = append(*selected, bytes.Clone(record.Path))
	proposal.Effect.ToggleSelection = true
	return nil
}

func savedSelectionPositions(snapshot Snapshot) string {
	state := snapshot.state
	var selected [][]byte
	if state.Source == protocol.SourceIndexed && state.Indexed != nil {
		selected = state.Indexed.SelectedPaths
	} else if state.Source == protocol.SourceBrowser && state.Browser != nil {
		selected = state.Browser.SelectedPaths
	}
	if len(selected) == 0 {
		return ""
	}
	positions := make(map[string]int, snapshot.RecordCount())
	for index, record := range snapshot.recordValues() {
		positions[string(record.Path)] = index + 1
	}
	restored := make([]string, 0, len(selected))
	for _, path := range selected {
		if position, ok := positions[string(path)]; ok {
			restored = append(restored, strconv.Itoa(position))
		}
	}
	return strings.Join(restored, ",")
}

func indexedHeader(picker protocol.Picker, filesOnly bool) string {
	source := indexedSourceName()
	if filesOnly {
		return source + " · files only"
	}
	if picker == protocol.PickerCD {
		return source + " · directories only"
	}
	return source
}
