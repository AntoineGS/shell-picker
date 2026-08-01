//go:build windows

package session

import "github.com/AntoineGS/shell-picker/internal/pathutil"

func addErrorHeader(state State) string {
	if state.Location.Kind != pathutil.KindDrives {
		return ""
	}
	return pathutil.PromptDisplay(state.Location)
}
