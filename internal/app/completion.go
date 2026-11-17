package app

import (
	"strings"

	"github.com/AntoineGS/shell-picker/internal/pathutil"
	"github.com/AntoineGS/shell-picker/internal/protocol"
	"github.com/AntoineGS/shell-picker/internal/session"
)

func commandLineHeader(command string) string {
	return strings.TrimRight(protocol.EscapeDisplay([]byte(command)), " ")
}

func completionOutcome(snapshot session.Snapshot, records [][]byte, options PickerOptions) (protocol.Outcome, error) {
	var outcome protocol.Outcome
	var err error
	if snapshot.State().ActivePicker() == protocol.PickerCD {
		outcome, err = session.ValidateCD(snapshot, records)
	} else {
		outcome, err = session.ValidateSearch(snapshot, records)
	}
	if err != nil {
		return outcome, err
	}
	outcome.Insert = snapshot.State().FilesSearch() && options.Picker != protocol.PickerSearch
	for index, path := range outcome.Paths {
		outcome.Paths[index] = []byte(pathutil.FormatCompletion(string(options.CWD), string(options.Home),
			string(snapshot.State().Query), string(path)))
	}
	return outcome, nil
}
