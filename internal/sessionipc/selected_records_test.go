package sessionipc

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestDecodeSelectedRecordsValidatesAndSplitsRows(t *testing.T) {
	first := protocol.WireRecord{Kind: protocol.KindFile, Display: "/one", Payload: protocol.EncodePath([]byte("/one"))}.Bytes()
	second := protocol.WireRecord{Kind: protocol.KindDirectory, Display: "/two", Payload: protocol.EncodePath([]byte("/two"))}.Bytes()
	var framed bytes.Buffer
	framed.Write(first)
	framed.WriteByte(0)
	framed.Write(second)
	framed.WriteByte(0)
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(framed.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	encoded := base64.StdEncoding.EncodeToString(compressed.Bytes())
	rows, err := decodeSelectedRecords(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || !bytes.Equal(rows[0], first) || !bytes.Equal(rows[1], second) {
		t.Fatalf("decoded selected rows = %q", rows)
	}
}

func TestDecodeSelectedRecordsRejectsMalformedFrame(t *testing.T) {
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	_, _ = writer.Write([]byte("invalid\x00"))
	_ = writer.Close()
	if _, err := decodeSelectedRecords(base64.StdEncoding.EncodeToString(compressed.Bytes())); err == nil {
		t.Fatal("malformed selected row accepted")
	}
}
