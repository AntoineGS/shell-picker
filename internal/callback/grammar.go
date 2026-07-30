package callback

import (
	"errors"
	"math"
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
)

type Command struct {
	Kind       Kind
	Opcode     protocol.Opcode
	Generation uint64
	Picker     protocol.Picker
}

func Parse(raw string) (Command, error) {
	events := map[string]protocol.Opcode{
		"e:mi": protocol.OpModeInsert, "e:ma": protocol.OpModeAdd, "e:es": protocol.OpEscape,
		"e:fw": protocol.OpForward, "e:up": protocol.OpParent, "e:sl": protocol.OpSlash,
		"e:hm": protocol.OpHome, "e:en": protocol.OpEnter,
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
	if !strings.HasPrefix(raw, "l:") {
		return Command{}, ErrGrammar
	}
	digits := strings.TrimPrefix(raw, "l:")
	if digits == "" || digits == "0" || digits[0] == '0' {
		return Command{}, ErrGrammar
	}
	for _, digit := range []byte(digits) {
		if digit < '0' || digit > '9' {
			return Command{}, ErrGrammar
		}
	}
	generation, err := strconv.ParseUint(digits, 10, 64)
	if err != nil || generation == 0 || generation > math.MaxUint64 {
		return Command{}, ErrGrammar
	}
	return Command{Kind: KindLoad, Generation: generation}, nil
}
