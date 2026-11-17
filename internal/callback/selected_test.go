package callback

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestReadSelectedRecordsFromFZFSelectionFile(t *testing.T) {
	first := protocol.WireRecord{Kind: protocol.KindFile, Display: "/a", Payload: protocol.EncodePath([]byte("/a"))}.Bytes()
	second := protocol.WireRecord{Kind: protocol.KindDirectory, Display: "/b", Payload: protocol.EncodePath([]byte("/b"))}.Bytes()
	path := filepath.Join(t.TempDir(), "selection")
	if err := os.WriteFile(path, append(append(first, '\n'), append(second, '\n')...), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readSelectedRecords(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || string(got[0]) != string(first) || string(got[1]) != string(second) {
		t.Fatalf("selected records = %q", got)
	}
}

func TestReadSelectedRecordsAcceptsNULFramedFZFSelectionFile(t *testing.T) {
	first := protocol.WireRecord{Kind: protocol.KindFile, Display: "/a", Payload: protocol.EncodePath([]byte("/a"))}.Bytes()
	second := protocol.WireRecord{Kind: protocol.KindDirectory, Display: "/b", Payload: protocol.EncodePath([]byte("/b"))}.Bytes()
	path := filepath.Join(t.TempDir(), "selection")
	if err := os.WriteFile(path, append(append(first, 0), append(second, 0)...), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readSelectedRecords(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || string(got[0]) != string(first) || string(got[1]) != string(second) {
		t.Fatalf("selected records = %q", got)
	}
}

func TestSelectedRecordsAreCompressedAsNULFramedWireRows(t *testing.T) {
	record := protocol.WireRecord{Kind: protocol.KindFile, Display: "/a", Payload: protocol.EncodePath([]byte("/a"))}.Bytes()
	encoded, err := encodeSelectedRecords([][]byte{record})
	if err != nil {
		t.Fatal(err)
	}
	compressed, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatal(err)
	}
	framed, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(framed, append(record, 0)) {
		t.Fatalf("framed selected records = %q", framed)
	}
}

func TestParseSelectedFileArgumentAcceptsFZFQuotedPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "selection")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	command, err := ParseSelectedFileArgument("e:sf --selected-file '" + path + "'")
	if err != nil {
		t.Fatal(err)
	}
	if command.Opcode != protocol.OpSourceToggle || len(command.SelectedRecords) != 0 {
		t.Fatalf("parsed command = %+v", command)
	}
}

func TestReadSelectedRecordsRejectsMalformedRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "selection")
	if err := os.WriteFile(path, []byte("not-a-wire-record\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readSelectedRecords(path); err == nil {
		t.Fatal("malformed selection file accepted")
	}
}
