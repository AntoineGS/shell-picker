package fzf

import "github.com/AntoineGS/shell-picker/internal/protocol"

func Options(picker protocol.Picker, prompt string) []string {
	options := []string{
		"--ansi",
		"--style=full",
		"--layout=reverse",
		"--delimiter=\t",
		"--with-nth=2",
		"--read0",
		"--print0",
		"--prompt=" + prompt,
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
		binding("start", startUnbind()),
	}
	switch picker {
	case protocol.PickerCD:
		return append(options, binding("space", clearMulti(), toggle()), "--sort", "--print-query", "--multi=1")
	case protocol.PickerCP:
		return append(options, binding("space", toggle()), "--no-sort", "--multi")
	default:
		return nil
	}
}
