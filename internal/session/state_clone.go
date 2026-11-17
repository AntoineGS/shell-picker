package session

import (
	"bytes"

	"github.com/AntoineGS/shell-picker/internal/pathutil"
)

func cloneState(state State) State {
	state.Location = cloneLocation(state.Location)
	state.Home = cloneLocation(state.Home)
	state.Query = bytes.Clone(state.Query)
	if state.Browser != nil {
		browser := *state.Browser
		browser.Location = cloneLocation(browser.Location)
		browser.Query = bytes.Clone(browser.Query)
		browser.SelectedPaths = clonePaths(browser.SelectedPaths)
		state.Browser = &browser
	}
	if state.Indexed != nil {
		indexed := *state.Indexed
		indexed.Query = bytes.Clone(indexed.Query)
		indexed.SelectedPaths = clonePaths(indexed.SelectedPaths)
		state.Indexed = &indexed
	}
	return state
}

func clonePaths(paths [][]byte) [][]byte {
	if paths == nil {
		return nil
	}
	cloned := make([][]byte, len(paths))
	for i, path := range paths {
		cloned[i] = bytes.Clone(path)
	}
	return cloned
}

func cloneLocation(location pathutil.Location) pathutil.Location {
	location.Path = bytes.Clone(location.Path)
	return location
}
