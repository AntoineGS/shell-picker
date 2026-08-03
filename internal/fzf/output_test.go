package fzf

import (
	"reflect"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestParseOutputSuccess(t *testing.T) {
	tests := []struct {
		name   string
		picker protocol.Picker
		raw    []byte
		want   Result
	}{
		{"cd", protocol.PickerCD, []byte("needle\x00enter\x00record\x00"), Result{Query: []byte("needle"), Key: "enter", Records: [][]byte{[]byte("record")}, ExitCode: 0}},
		{"cd empty query", protocol.PickerCD, []byte("\x00enter\x00record\x00"), Result{Query: []byte{}, Key: "enter", Records: [][]byte{[]byte("record")}, ExitCode: 0}},
		{"cp", protocol.PickerCP, []byte("enter\x00one\x00two\x00"), Result{Key: "enter", Records: [][]byte{[]byte("one"), []byte("two")}, ExitCode: 0}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ParseOutput(test.picker, test.raw, 0)
			if err != nil || !reflect.DeepEqual(got, test.want) {
				t.Fatalf("got=%+v want=%+v err=%v", got, test.want, err)
			}
		})
	}
}

func TestParseOutputAbort(t *testing.T) {
	tests := []struct {
		picker protocol.Picker
		raw    []byte
		code   int
		query  []byte
	}{
		{protocol.PickerCD, []byte("\x00"), 130, []byte{}},
		{protocol.PickerCD, []byte{}, 130, nil},
		{protocol.PickerCP, []byte{}, 130, nil},
	}
	for _, test := range tests {
		got, err := ParseOutput(test.picker, test.raw, test.code)
		if err != nil || !got.Aborted || got.ExitCode != test.code || !reflect.DeepEqual(got.Query, test.query) {
			t.Fatalf("picker=%q raw=%q code=%d got=%+v err=%v", test.picker, test.raw, test.code, got, err)
		}
	}
}

func TestParseOutputRejectsGenericNoMatchExit(t *testing.T) {
	for _, test := range []struct {
		name   string
		picker protocol.Picker
		raw    []byte
	}{
		{name: "cd query", picker: protocol.PickerCD, raw: []byte("needle\x00")},
		{name: "cp empty", picker: protocol.PickerCP},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := ParseOutput(test.picker, test.raw, 1)
			if err == nil || got.Aborted {
				t.Fatalf("got=%+v err=%v, want non-aborted no-match error", got, err)
			}
		})
	}
}

func TestParseOutputRejectsMalformedFrames(t *testing.T) {
	tests := []struct {
		name   string
		picker protocol.Picker
		raw    []byte
		code   int
	}{
		{"trailing bytes", protocol.PickerCP, []byte("enter\x00record"), 0},
		{"non-enter", protocol.PickerCP, []byte("tab\x00record\x00"), 0},
		{"empty record", protocol.PickerCP, []byte("enter\x00\x00"), 0},
		{"cd no record", protocol.PickerCD, []byte("q\x00enter\x00"), 0},
		{"cd many records", protocol.PickerCD, []byte("q\x00enter\x00one\x00two\x00"), 0},
		{"cp no records", protocol.PickerCP, []byte("enter\x00"), 0},
		{"bad abort cp", protocol.PickerCP, []byte("x\x00"), 130},
		{"bad abort cd", protocol.PickerCD, []byte("q\x00extra\x00"), 1},
		{"unexpected exit", protocol.PickerCP, nil, 2},
		{"unknown picker", protocol.Picker("bad"), []byte("enter\x00x\x00"), 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ParseOutput(test.picker, test.raw, test.code); err == nil {
				t.Fatal("ParseOutput succeeded")
			}
		})
	}
}
