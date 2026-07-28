package protocol

import (
	"bytes"
	"testing"
)

func TestEncodeOutcomeNUL(t *testing.T) {
	tests := []struct {
		name    string
		outcome Outcome
		want    []byte
	}{
		{"accepted", Outcome{Status: StatusAccepted, Paths: [][]byte{[]byte("one"), []byte("line\nname"), {0xff}}}, []byte("one\x00line\nname\x00\xff\x00")},
		{"aborted", Outcome{Status: StatusAborted, Paths: [][]byte{[]byte("ignored")}}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got bytes.Buffer
			if err := EncodeOutcome(&got, OutputNUL, tc.outcome); err != nil {
				t.Fatalf("EncodeOutcome() error = %v", err)
			}
			if !bytes.Equal(got.Bytes(), tc.want) {
				t.Fatalf("EncodeOutcome() = %q; want %q", got.Bytes(), tc.want)
			}
		})
	}
}

func TestEncodeOutcomeNUON(t *testing.T) {
	tests := []struct {
		name    string
		outcome Outcome
		want    string
	}{
		{"accepted", Outcome{Status: StatusAccepted, Paths: [][]byte{[]byte("path"), []byte("line\nname")}}, "{\"status\":\"accepted\",\"paths\":[\"path\",\"line\\nname\"]}\n"},
		{"aborted", Outcome{Status: StatusAborted}, "{\"status\":\"aborted\",\"paths\":[]}\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got bytes.Buffer
			if err := EncodeOutcome(&got, OutputNUON, tc.outcome); err != nil {
				t.Fatalf("EncodeOutcome() error = %v", err)
			}
			if got.String() != tc.want {
				t.Fatalf("EncodeOutcome() = %q; want %q", got.String(), tc.want)
			}
		})
	}
}

func TestEncodeOutcomeRejectsInvalidNUONPathAndUnknownValues(t *testing.T) {
	tests := []struct {
		name    string
		format  OutputFormat
		outcome Outcome
	}{
		{"invalid UTF-8", OutputNUON, Outcome{Status: StatusAccepted, Paths: [][]byte{{0xff}}}},
		{"unknown format", OutputFormat("other"), Outcome{Status: StatusAccepted}},
		{"unknown status", OutputNUL, Outcome{Status: Status("other")}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := EncodeOutcome(&bytes.Buffer{}, tc.format, tc.outcome); err == nil {
				t.Fatal("EncodeOutcome() unexpectedly succeeded")
			}
		})
	}
}
