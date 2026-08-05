package finderinfo

import (
	"errors"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestFormat(t *testing.T) {
	tests := []struct {
		name           string
		picker         protocol.Picker
		matched, total int
		selected       int
		want           string
	}{
		{name: "cd ignores selection display", picker: protocol.PickerCD, matched: 7, total: 42, selected: 1, want: "7/42"},
		{name: "cp without selection", picker: protocol.PickerCP, matched: 7, total: 42, want: "7/42"},
		{name: "cp with selection", picker: protocol.PickerCP, matched: 7, total: 42, selected: 2, want: "7/42 (2)"},
		{name: "zero counts", picker: protocol.PickerCD, want: "0/0"},
		{name: "maximum counts", picker: protocol.PickerCP, matched: MaxCount, total: MaxCount, selected: MaxCount, want: "1000000000/1000000000 (1000000000)"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := Format(test.picker, test.matched, test.total, test.selected)
			if err != nil {
				t.Fatalf("Format() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("Format() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestFormatRejectsInvalidCounts(t *testing.T) {
	tests := []struct {
		name           string
		matched, total int
		selected       int
	}{
		{name: "negative matched", matched: -1, total: 1},
		{name: "negative total", matched: 0, total: -1},
		{name: "negative selected", matched: 0, total: 1, selected: -1},
		{name: "matched exceeds maximum", matched: MaxCount + 1, total: MaxCount + 1},
		{name: "total exceeds maximum", matched: 0, total: MaxCount + 1},
		{name: "selected exceeds maximum", matched: 0, total: MaxCount, selected: MaxCount + 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Format(protocol.PickerCP, test.matched, test.total, test.selected)
			if !errors.Is(err, ErrInvalidCount) {
				t.Fatalf("Format() error = %v, want ErrInvalidCount", err)
			}
		})
	}
}

func TestFormatAcceptsIndividuallyValidCountsWithRelationalMismatches(t *testing.T) {
	for _, test := range []struct {
		name           string
		matched, total int
		selected       int
		want           string
	}{
		{name: "matched exceeds total", matched: 2, total: 1, want: "2/1"},
		{name: "selected exceeds total", matched: 1, total: 1, selected: 2, want: "1/1 (2)"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := Format(protocol.PickerCP, test.matched, test.total, test.selected)
			if err != nil || got != test.want {
				t.Fatalf("Format()=(%q,%v), want (%q,nil)", got, err, test.want)
			}
		})
	}
}

func TestFormatRejectsUnknownPicker(t *testing.T) {
	if _, err := Format(protocol.Picker("unknown"), 0, 0, 0); err == nil {
		t.Fatal("Format() accepted an unknown picker")
	}
}
