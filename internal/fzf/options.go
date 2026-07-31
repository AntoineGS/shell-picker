package fzf

import "github.com/AntoineGS/shell-picker/internal/protocol"

func Options(picker protocol.Picker, prompt, header string) []string {
	var infoCommand string
	switch picker {
	case protocol.PickerCD:
		infoCommand = "--info-command=i:cd"
	case protocol.PickerCP:
		infoCommand = "--info-command=i:cp"
	default:
		return nil
	}
	options := []string{
		"--ansi",
		"--style=full",
		"--layout=reverse",
		"--delimiter=\t",
		"--with-nth=2",
		"--read0",
		"--print0",
		"--prompt=" + prompt,
		"--header=" + header,
		"--header-first",
		infoCommand,
		"--preview=p",
		"--preview-window=right:50%:wrap",
		binding("enter", transformEvent(protocol.OpEnter)),
		binding("esc", transformEvent(protocol.OpEscape)),
		binding("i", transformEvent(protocol.OpModeInsert)),
		binding("a", transformEvent(protocol.OpModeAdd)),
		binding("ctrl-l,tab,right", transformEvent(protocol.OpForward)),
		binding("ctrl-h,left", transformEvent(protocol.OpParent)),
		binding("/", transformEvent(protocol.OpSlash)),
		binding("~", transformEvent(protocol.OpHome)),
		binding("j", down()),
		binding("k", up()),
		binding("h", trigger("ctrl-h")),
		binding("l", trigger("tab")),
		binding("q", abort()),
		binding("ctrl-u", halfPageUp()),
		binding("ctrl-d", halfPageDown()),
		binding(",", previewHalfPageUp()),
		binding(".", previewHalfPageDown()),
		binding("change", transformEvent(protocol.OpRestoreView)),
		binding("result-final", keyAction("rebind", []string{"change"}), keyAction("unbind", []string{"result-final"})),
		binding("start", startUnbind(), transformDisplay()),
		binding("resize", transformDisplay()),
	}
	for _, key := range normalPrintableKeys {
		options = append(options, binding(key, ignore()))
	}
	if picker == protocol.PickerCD {
		return append(options, binding("space", clearMulti(), toggle()), "--sort", "--print-query", "--multi=1")
	}
	return append(options, binding("space", toggle()), "--no-sort", "--multi")
}

func transformDisplay() action { return action{text: "transform(d)"} }
