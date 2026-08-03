package fzf

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/AntoineGS/shell-picker/internal/protocol"
)

type Result struct {
	Query    []byte
	Key      string
	Records  [][]byte
	Aborted  bool
	ExitCode int
}

func ParseOutput(picker protocol.Picker, output []byte, exitCode int) (Result, error) {
	if picker != protocol.PickerCD && picker != protocol.PickerCP {
		return Result{}, fmt.Errorf("fzf: invalid picker %q", picker)
	}
	if exitCode != 0 && exitCode != 1 && exitCode != 130 {
		return Result{}, fmt.Errorf("fzf: unexpected exit code %d", exitCode)
	}
	if exitCode == 1 {
		return Result{}, errors.New("fzf: no match or search error (exit code 1)")
	}
	frames, err := nulFrames(output)
	if err != nil {
		return Result{}, err
	}
	if exitCode == 130 {
		result := Result{Aborted: true, ExitCode: exitCode}
		switch picker {
		case protocol.PickerCD:
			if exitCode == 130 && len(frames) == 0 {
				return result, nil
			}
			if len(frames) != 1 {
				return Result{}, errors.New("fzf: malformed cd abort output")
			}
			result.Query = frames[0]
		case protocol.PickerCP:
			if len(frames) != 0 {
				return Result{}, errors.New("fzf: malformed cp abort output")
			}
		}
		return result, nil
	}

	result := Result{ExitCode: exitCode}
	switch picker {
	case protocol.PickerCD:
		if len(frames) != 3 {
			return Result{}, errors.New("fzf: cd acceptance requires query, key, and one record")
		}
		result.Query, result.Key, result.Records = frames[0], string(frames[1]), frames[2:]
	case protocol.PickerCP:
		if len(frames) < 2 {
			return Result{}, errors.New("fzf: cp acceptance requires key and records")
		}
		result.Key, result.Records = string(frames[0]), frames[1:]
	}
	if result.Key != "enter" {
		return Result{}, fmt.Errorf("fzf: unsupported acceptance key %q", result.Key)
	}
	for _, record := range result.Records {
		if len(record) == 0 {
			return Result{}, errors.New("fzf: empty record frame")
		}
	}
	return result, nil
}

func nulFrames(output []byte) ([][]byte, error) {
	if len(output) == 0 {
		return nil, nil
	}
	if output[len(output)-1] != 0 {
		return nil, errors.New("fzf: output has trailing bytes without NUL")
	}
	parts := bytes.Split(output[:len(output)-1], []byte{0})
	frames := make([][]byte, len(parts))
	for i, part := range parts {
		frames[i] = bytes.Clone(part)
	}
	return frames, nil
}
