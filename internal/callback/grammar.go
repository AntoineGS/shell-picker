package callback

import (
	"encoding/base64"
	"errors"
	"strconv"
	"strings"

	"github.com/AntoineGS/shell-picker/internal/protocol"
)

var ErrGrammar = errors.New("callback: invalid command grammar")

type Kind uint8

const (
	KindEvent Kind = iota + 1
	KindLoad
	KindPreview
	KindDisplay
	KindInfo
	KindEmptySource
	KindInvalidPreview
	KindLiteral
)

type Command struct {
	Text            string
	Kind            Kind
	Opcode          protocol.Opcode
	Generation      uint64
	EventID         uint64
	Picker          protocol.Picker
	SelectedRecords [][]byte
}

func (command Command) Local() bool {
	return command.Kind == KindInfo || command.Kind == KindEmptySource || command.Kind == KindInvalidPreview || command.Kind == KindLiteral
}

func Parse(raw string) (Command, error) {
	if encoded, ok := strings.CutPrefix(raw, "v:"); ok {
		text, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
		if err != nil || len(text) > 32768 {
			return Command{}, ErrGrammar
		}
		return Command{Kind: KindLiteral, Text: string(text)}, nil
	}
	events := map[string]protocol.Opcode{
		"e:mi": protocol.OpModeInsert, "e:ma": protocol.OpModeAdd, "e:es": protocol.OpEscape,
		"e:fw": protocol.OpForward, "e:up": protocol.OpParent, "e:sl": protocol.OpSlash,
		"e:hm": protocol.OpHome, "e:en": protocol.OpEnter, "e:rs": protocol.OpRestoreView,
		"e:sf": protocol.OpSourceToggle, "e:mk": protocol.OpMarkToggle,
		"e:fa": protocol.OpFilesSearch,
		"e:qy": protocol.OpQuery,
		"e:il": protocol.OpInputLoaded,
	}
	if opcode, ok := events[raw]; ok {
		return Command{Kind: KindEvent, Opcode: opcode}, nil
	}
	if raw == "p" {
		return Command{Kind: KindPreview}, nil
	}
	if raw == "d" {
		return Command{Kind: KindDisplay}, nil
	}
	if raw == "i:cd" {
		return Command{Kind: KindInfo, Picker: protocol.PickerCD}, nil
	}
	if raw == "i:cp" {
		return Command{Kind: KindInfo, Picker: protocol.PickerCP}, nil
	}
	if raw == "l:empty" {
		return Command{Kind: KindEmptySource}, nil
	}
	if raw == "p:invalid" {
		return Command{Kind: KindInvalidPreview}, nil
	}
	if !strings.HasPrefix(raw, "l:") {
		return Command{}, ErrGrammar
	}
	digits := strings.TrimPrefix(raw, "l:")
	parts := strings.Split(digits, ":")
	if len(parts) > 2 || parts[0] == "" || parts[0] == "0" || parts[0][0] == '0' {
		return Command{}, ErrGrammar
	}
	for _, digit := range []byte(parts[0]) {
		if digit < '0' || digit > '9' {
			return Command{}, ErrGrammar
		}
	}
	generation, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil || generation == 0 {
		return Command{}, ErrGrammar
	}
	var eventID uint64
	if len(parts) == 2 {
		if parts[1] == "" || parts[1] == "0" || parts[1][0] == '0' {
			return Command{}, ErrGrammar
		}
		for _, digit := range []byte(parts[1]) {
			if digit < '0' || digit > '9' {
				return Command{}, ErrGrammar
			}
		}
		eventID, err = strconv.ParseUint(parts[1], 10, 64)
		if err != nil || eventID == 0 {
			return Command{}, ErrGrammar
		}
	}
	return Command{Kind: KindLoad, Generation: generation, EventID: eventID}, nil
}
