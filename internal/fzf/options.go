package fzf

import (
	"errors"
	"runtime"
	"strconv"
	"strings"

	"github.com/AntoineGS/shell-picker/internal/protocol"
)

// OptionsConfig describes the stable and optional listen-specific fzf flags.
type OptionsConfig struct {
	Completion     bool
	Query          string
	Height         string
	Picker         protocol.Picker
	Prompt         string
	Header         string
	ListenAddress  string
	InitialLoading bool
}

var errInvalidListenAddress = errors.New("fzf: invalid listen address")

// Options returns the fzf arguments for a picker configuration.
func Options(config OptionsConfig) ([]string, error) {
	return optionsForPlatform(config, runtime.GOOS)
}

func optionsForPlatform(config OptionsConfig, goos string) ([]string, error) {
	switch config.Picker {
	case protocol.PickerCD, protocol.PickerCP, protocol.PickerSearch:
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
	if config.Height != "" {
		options = append(options, "--height="+config.Height)
	}
	if config.Completion || config.InitialLoading {
		options = append(options, "--query="+config.Query)
	}
	if listenEnabled {
		options = append(options, "--info=hidden", "--list-label=0/0", "--listen="+config.ListenAddress)
	} else if goos == "windows" {
		options = append(options, "--info=inline-right")
	} else {
		options = append(options, pickerInfoCommand(config.Picker))
	}
	forwardKeys, normalForward := "ctrl-l,tab,right", "tab"
	if listenEnabled {
		forwardKeys, normalForward = "ctrl-l,right", "right"
	}
	if !config.InitialLoading {
		options = append(options, "--preview=p")
	}
	options = append(options,
		"--preview-window=right:50%:wrap:<80(down:50%:wrap)",
		binding("enter", transformEvent(protocol.OpEnter)),
		binding("ctrl-f", transformEvent(protocol.OpSourceToggle)),
		// Terminals can map Ctrl+Shift+F to this distinct, portable sequence.
		binding("ctrl-alt-f", transformEvent(protocol.OpFilesSearch)),
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
	)
	if config.InitialLoading {
		options = append(options, binding("double-click", transformEvent(protocol.OpEnter)))
	}
	start := []action{startUnbind()}
	if config.Completion {
		start = []action{keyAction("unbind", normalKeys), transformEvent(protocol.OpQuery)}
		options = append(options, binding("change", transformEvent(protocol.OpQuery)))
	}
	if config.InitialLoading {
		loadingKeys := []string{"ctrl-f", "ctrl-alt-f", "ctrl-l", "tab", "right", "ctrl-h", "left", "/", "~",
			"shift-scroll-up", "shift-scroll-down"}
		start = []action{startUnbind(), keyAction("unbind", loadingKeys)}
		if config.Completion {
			loadingKeys = append(loadingKeys, "change")
		}
		options = append(options, binding("load", keyAction("unbind", []string{"load"}),
			transformEvent(protocol.OpInputLoaded), keyAction("rebind", loadingKeys), changePreviewDefault()))
	} else if !listenEnabled && goos != "windows" {
		start = append(start, transformDisplay())
	}
	options = append(options, binding("start", start...))
	if !listenEnabled && goos != "windows" {
		options = append(options, binding("resize", transformDisplay()))
	}
	for _, key := range normalPrintableKeys {
		options = append(options, binding(key, ignore()))
	}
	if config.Picker == protocol.PickerCD {
		mark := transformEvent(protocol.OpMarkToggle)
		return append(options,
			binding("space", mark),
			binding("ctrl-space", mark),
			binding("btab", transformEvent(protocol.OpMarkToggle), up()),
			binding("right-click,shift-right-click", mark),
			binding("shift-left-click", ignore()),
			"--sort", "--print-query", "--multi=1"), nil
	}
	mark := transformEvent(protocol.OpMarkToggle)
	return append(options,
		binding("space", mark),
		binding("ctrl-space", mark),
		binding("btab", transformEvent(protocol.OpMarkToggle), up()),
		binding("right-click,shift-right-click", mark),
		binding("shift-left-click", ignore()),
		"--no-sort", "--multi"), nil
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
