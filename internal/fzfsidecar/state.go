package fzfsidecar

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"

	"github.com/AntoineGS/shell-picker/internal/finderinfo"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

type fzfStateWire struct {
	TotalCount *int            `json:"totalCount"`
	MatchCount *int            `json:"matchCount"`
	Selected   json.RawMessage `json:"selected"`
}

type fzfState struct {
	picker    protocol.Picker
	matched   int
	total     int
	selected  int
	formatted string
}

type fzfPage struct {
	matched  int
	total    int
	selected int
}

func newFZFState(picker protocol.Picker, matched, total, selected int) (fzfState, error) {
	state := fzfState{picker: picker, matched: matched, total: total, selected: selected}
	formatted, err := state.renderedLabel()
	if err != nil {
		return fzfState{}, errInvalidState
	}
	state.formatted = formatted
	return state, nil
}

func (state fzfState) renderedLabel() (string, error) {
	if err := state.validate(); err != nil {
		return "", err
	}
	return finderinfo.Format(state.picker, state.matched, state.total, state.selected)
}

func (state fzfState) validate() error {
	if err := finderinfo.ValidateCounts(state.picker, state.matched, state.total, state.selected); err != nil {
		return errInvalidState
	}
	if state.matched > state.total || state.selected > state.total {
		return errInvalidState
	}
	return nil
}

func (state fzfState) actionBody() ([]byte, error) {
	formatted, err := state.renderedLabel()
	if err != nil {
		return nil, errInvalidAction
	}
	return append([]byte("change-list-label:"), formatted...), nil
}

func decodeState(body []byte, picker protocol.Picker) (fzfState, error) {
	page, err := decodePage(body)
	if err != nil {
		return fzfState{}, err
	}
	return newFZFState(picker, page.matched, page.total, page.selected)
}

func decodePage(body []byte) (fzfPage, error) {
	var wire fzfStateWire
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&wire); err != nil {
		return fzfPage{}, errors.Join(errInvalidJSON, errInvalidState)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return fzfPage{}, errInvalidJSON
	}
	if wire.TotalCount == nil || wire.MatchCount == nil || len(wire.Selected) == 0 || bytes.Equal(bytes.TrimSpace(wire.Selected), []byte("null")) {
		return fzfPage{}, errInvalidState
	}
	selected, err := countSelected(wire.Selected)
	if err != nil {
		return fzfPage{}, errInvalidState
	}
	return fzfPage{matched: *wire.MatchCount, total: *wire.TotalCount, selected: selected}, nil
}

func countSelected(raw json.RawMessage) (int, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('[') {
		return 0, errInvalidState
	}

	count := 0
	for decoder.More() {
		var item json.RawMessage
		if err := decoder.Decode(&item); err != nil {
			return 0, errInvalidState
		}
		if count == finderinfo.MaxCount {
			return 0, errInvalidState
		}
		count++
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim(']') {
		return 0, errInvalidState
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return 0, errInvalidState
	}
	return count, nil
}

func parseFormattedLabel(label string) (fzfState, error) {
	slash := strings.IndexByte(label, '/')
	if slash <= 0 || strings.Count(label, "/") != 1 {
		return fzfState{}, errInvalidAction
	}

	matched, ok := parseCanonicalCount(label[:slash])
	if !ok {
		return fzfState{}, errInvalidAction
	}
	totalText := label[slash+1:]
	picker := protocol.PickerCD
	selected := 0
	if marker := strings.Index(totalText, " ("); marker >= 0 {
		if strings.Count(totalText, " (") != 1 || !strings.HasSuffix(totalText, ")") {
			return fzfState{}, errInvalidAction
		}
		selectedText := totalText[marker+2 : len(totalText)-1]
		var selectedOK bool
		selected, selectedOK = parseCanonicalCount(selectedText)
		if !selectedOK || selected == 0 {
			return fzfState{}, errInvalidAction
		}
		totalText = totalText[:marker]
		picker = protocol.PickerCP
	}
	total, ok := parseCanonicalCount(totalText)
	if !ok {
		return fzfState{}, errInvalidAction
	}
	state, err := newFZFState(picker, matched, total, selected)
	if err != nil || state.formatted != label {
		return fzfState{}, errInvalidAction
	}
	return state, nil
}

func parseCanonicalCount(value string) (int, bool) {
	if value == "" || len(value) > 1 && value[0] == '0' {
		return 0, false
	}
	for _, digit := range []byte(value) {
		if digit < '0' || digit > '9' {
			return 0, false
		}
	}
	count, err := strconv.Atoi(value)
	return count, err == nil
}
