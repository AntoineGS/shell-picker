package protocol

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"
)

func EncodeOutcome(w io.Writer, format OutputFormat, outcome Outcome) error {
	if outcome.Status != StatusAccepted && outcome.Status != StatusAborted {
		return fmt.Errorf("invalid outcome status %q", outcome.Status)
	}

	switch format {
	case OutputNUL:
		return encodeNULOutcome(w, outcome)
	case OutputNUON:
		return encodeNUONOutcome(w, outcome)
	default:
		return fmt.Errorf("invalid output format %q", format)
	}
}

func encodeNULOutcome(w io.Writer, outcome Outcome) error {
	if outcome.Status == StatusAborted {
		return nil
	}
	for _, path := range outcome.Paths {
		if err := writeAll(w, path); err != nil {
			return err
		}
		if err := writeAll(w, []byte{0}); err != nil {
			return err
		}
	}
	return nil
}

func encodeNUONOutcome(w io.Writer, outcome Outcome) error {
	paths := make([]string, 0)
	if outcome.Status == StatusAccepted {
		paths = make([]string, len(outcome.Paths))
		for i, path := range outcome.Paths {
			if !utf8.Valid(path) {
				return errors.New("NUON output path is not valid UTF-8")
			}
			paths[i] = string(path)
		}
	}

	wire := struct {
		Status Status   `json:"status"`
		Paths  []string `json:"paths"`
	}{Status: outcome.Status, Paths: paths}
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(wire); err != nil {
		return fmt.Errorf("encode NUON outcome: %w", err)
	}
	return nil
}
