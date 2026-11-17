package callback

import (
	"errors"

	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func ValidateLocal(command Command, lookupEnv func(string) string) error {
	if lookupEnv == nil {
		return errors.New("callback: nil environment reader")
	}
	switch command.Kind {
	case KindEvent:
		if command.Opcode != protocol.OpRestoreView &&
			!validKey(command.Opcode, lookupEnv("FZF_KEY")) {
			return ErrKey
		}
	case KindLoad, KindPreview, KindDisplay, KindInfo, KindEmptySource, KindInvalidPreview:
		return nil
	default:
		return ErrGrammar
	}
	return nil
}

func validKey(opcode protocol.Opcode, key string) bool {
	allowed := map[protocol.Opcode]map[string]bool{
		protocol.OpModeInsert: {"i": true}, protocol.OpModeAdd: {"a": true}, protocol.OpEscape: {"esc": true},
		protocol.OpForward: {"ctrl-l": true, "tab": true, "right": true, "l": true},
		protocol.OpParent:  {"ctrl-h": true, "ctrl-backspace": true, "left": true, "h": true},
		protocol.OpSlash:   {"/": true}, protocol.OpHome: {"~": true}, protocol.OpEnter: {"enter": true},
		protocol.OpSourceToggle: {"ctrl-f": true},
		protocol.OpFilesSearch:  {"ctrl-alt-f": true},
		protocol.OpMarkToggle: {"space": true, "ctrl-space": true, "btab": true,
			"shift-tab": true, "right-click": true, "shift-right-click": true},
	}
	keys, ok := allowed[opcode]
	return ok && keys[key]
}
