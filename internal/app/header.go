package app

import (
	"github.com/AntoineGS/shell-picker/internal/pathutil"
	"github.com/AntoineGS/shell-picker/internal/protocol"
	"github.com/AntoineGS/shell-picker/internal/session"
)

func pickerHeader(command string, state session.State, indexed *indexedSource) string {
	if state.Source == protocol.SourceIndexed && indexed != nil {
		return commandStatusHeader(command, indexed.header(state.ActivePicker(), state.FilesSearch()))
	}
	if command != "" {
		return command
	}
	return pathutil.PromptDisplayHome(state.Location, state.Home)
}

func commandStatusHeader(command, status string) string {
	if command == "" {
		return status
	}
	return command + " · " + status
}

func (backend *pickerBackend) decorateHeader(effect *protocol.Effect, state session.State) {
	if effect.Header == "" {
		return
	}
	if effect.ErrorPrompt {
		effect.Header = commandStatusHeader(backend.commandHeader, effect.Header)
		return
	}
	effect.Header = pickerHeader(backend.commandHeader, state, backend.indexed)
}
