package fzf

import (
	"errors"
	"strconv"
	"strings"

	"github.com/AntoineGS/shell-picker/internal/protocol"
)

// OptionsConfig describes the stable and optional listen-specific fzf flags.
type OptionsConfig struct {
	Picker        protocol.Picker
	Prompt        string
	Header        string
	ListenAddress string
}

var errInvalidListenAddress = errors.New("fzf: invalid listen address")

// Options returns the fzf arguments for a picker configuration.
func Options(config OptionsConfig) ([]string, error) {
	switch config.Picker {
	case protocol.PickerCD, protocol.PickerCP:
	default:
		return nil, nil
	}
	if config.ListenAddress != "" {
		if err := validateListenAddress(config.ListenAddress); err != nil {
			return nil, err
		}
	}
	listenEnabled := config.ListenAddress != ""
	options := []string{
		"--ansi",
		"--style=full",
		"--layout=reverse",
		"--delimiter=\t",
		"--with-nth=2",
		"--keep-right",
		"--jump-labels=g",
		"--read0",
		"--print0",
		"--prompt=" + config.Prompt,
		"--header=" + config.Header,
		"--header-first",
	}
	if listenEnabled {
		options = append(options, "--info=hidden", "--list-label=0/0", "--listen="+config.ListenAddress)
	} else {
		options = append(options, pickerInfoCommand(config.Picker))
	}
	forwardKeys, normalForward := "ctrl-l,tab,right", "tab"
	if listenEnabled {
		forwardKeys, normalForward = "ctrl-l,right", "right"
	}
	options = append(options,
		"--preview=p",
		"--preview-window=right:50%:wrap:<80(down:50%:wrap)",
		binding("enter", transformEvent(protocol.OpEnter)),
		binding("esc", transformEvent(protocol.OpEscape)),
		binding("i", transformEvent(protocol.OpModeInsert)),
		binding("a", transformEvent(protocol.OpModeAdd)),
		binding(forwardKeys, transformEvent(protocol.OpForward)),
		binding("ctrl-h,left", transformEvent(protocol.OpParent)),
		binding("/", transformEvent(protocol.OpSlash)),
		binding("~", transformEvent(protocol.OpHome)),
		binding("g", jump()),
		binding("G", last()),
		binding("jump", first()),
		binding("j", down()),
		binding("k", up()),
		binding("h", trigger("ctrl-h")),
		binding("l", trigger(normalForward)),
		binding("q", abort()),
		binding("ctrl-u", halfPageUp()),
		binding("ctrl-d", halfPageDown()),
		binding(",", previewHalfPageUp()),
		binding(".", previewHalfPageDown()),
		binding("change", transformEvent(protocol.OpRestoreView)),
		binding("result-final", keyAction("rebind", []string{"change"}), keyAction("unbind", []string{"result-final"})),
	)
	start := []action{startUnbind()}
	if !listenEnabled {
		start = append(start, transformDisplay())
	}
	options = append(options, binding("start", start...))
	if !listenEnabled {
		options = append(options, binding("resize", transformDisplay()))
	}
	for _, key := range normalPrintableKeys {
		options = append(options, binding(key, ignore()))
	}
	if config.Picker == protocol.PickerCD {
		return append(options, binding("space", clearMulti(), toggle()), "--sort", "--print-query", "--multi=1"), nil
	}
	return append(options, binding("space", toggle()), "--no-sort", "--multi"), nil
}

func transformDisplay() action { return action{text: "transform(d)"} }

func pickerInfoCommand(picker protocol.Picker) string {
	if picker == protocol.PickerCD {
		return "--info-command=i:cd"
	}
	return "--info-command=i:cp"
}

func validateListenAddress(address string) error {
	const prefix = "127.0.0.1:"
	if !strings.HasPrefix(address, prefix) {
		return errInvalidListenAddress
	}
	portText := strings.TrimPrefix(address, prefix)
	if portText == "" || len(portText) > 1 && portText[0] == '0' {
		return errInvalidListenAddress
	}
	for index := range len(portText) {
		if portText[index] < '0' || portText[index] > '9' {
			return errInvalidListenAddress
		}
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port <= 0 || port > 65535 || strconv.Itoa(port) != portText {
		return errInvalidListenAddress
	}
	return nil
}
