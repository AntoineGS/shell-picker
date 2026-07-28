package protocol

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

func TestPathCodecAndDisplay(t *testing.T) {
	cases := []struct {
		name    string
		raw     []byte
		display string
	}{
		{"tab", []byte("tab\tname"), `tab\tname`},
		{"newline", []byte("line\nname"), `line\nname`},
		{"trailing-space", []byte("trailing "), "trailing "},
		{"backslash", []byte(`back\slash`), `back\\slash`},
		{"nbsp", []byte("nbsp\u00a0name"), "nbsp\u00a0name"},
		{"leading-dash", []byte("-leading"), "-leading"},
		{"control", []byte{'c', 1, 'x'}, `c\x01x`},
		{"ending-newline", []byte("ending-newline\n"), `ending-newline\n`},
		{"apostrophe", []byte("apostrophe'path"), `apostrophe\'path`},
		{"invalid-utf8", []byte{'x', 0xff}, `x\xFF`},
	}
	for _, tc := range cases {
		payload := EncodePath(tc.raw)
		got, err := DecodePath(payload)
		if err != nil || !bytes.Equal(got, tc.raw) || EscapeDisplay(tc.raw) != tc.display {
			t.Fatalf("%s payload=%q got=%q display=%q err=%v", tc.name, payload, got, EscapeDisplay(tc.raw), err)
		}
	}
	for _, bad := range []string{"", "not%base64", "YQ", "YQ==junk"} {
		if _, err := DecodePath(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestRecordRequiresExactlyTwoTabsAndNULFrames(t *testing.T) {
	record := WireRecord{Kind: KindFile, Display: `a\tb`, Payload: EncodePath([]byte("a\tb"))}
	framed := FrameRecords([]WireRecord{record})
	if bytes.Count(record.Bytes(), []byte{'\t'}) != 2 || framed[len(framed)-1] != 0 {
		t.Fatalf("record=%q framed=%q", record.Bytes(), framed)
	}
	for _, bad := range [][]byte{[]byte("file\tone"), []byte("file\tone\ttwo\tthree"), []byte("bad\x00x\tp\tq")} {
		if _, err := ParseRecord(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestParseRecordStrictValidation(t *testing.T) {
	want := WireRecord{Kind: KindDirectory, Display: "directory", Payload: EncodePath([]byte("directory"))}
	got, err := ParseRecord(want.Bytes())
	if err != nil || got != want {
		t.Fatalf("ParseRecord() = %#v, %v; want %#v", got, err, want)
	}

	for _, bad := range [][]byte{
		[]byte("unknown\tdisplay\tYQ=="),
		[]byte("file\tdisplay\t"),
		[]byte("file\tdisplay\tYQ"),
		[]byte("file\tdisplay\tYR=="),
		[]byte("file\tdisplay\tYQ==\x00"),
	} {
		if _, err := ParseRecord(bad); err == nil {
			t.Errorf("ParseRecord(%q) unexpectedly succeeded", bad)
		}
	}
}

func TestFrameRecordsUsesNULWithoutNewlines(t *testing.T) {
	records := []WireRecord{
		{Kind: KindLocal, Display: "one", Payload: EncodePath([]byte("one"))},
		{Kind: KindDrive, Display: "two", Payload: EncodePath([]byte("two"))},
	}
	want := append(append(append([]byte{}, records[0].Bytes()...), 0), records[1].Bytes()...)
	want = append(want, 0)
	if got := FrameRecords(records); !bytes.Equal(got, want) || bytes.Contains(got, []byte{'\n'}) {
		t.Fatalf("FrameRecords() = %q; want %q", got, want)
	}
}

func TestWriteFramedRecordsPropagatesWriterFailure(t *testing.T) {
	records := []WireRecord{{Kind: KindFile, Display: "a", Payload: EncodePath([]byte("a"))}}
	var got bytes.Buffer
	if err := WriteFramedRecords(&got, records); err != nil {
		t.Fatalf("WriteFramedRecords() error = %v", err)
	}
	if want := FrameRecords(records); !bytes.Equal(got.Bytes(), want) {
		t.Fatalf("WriteFramedRecords() = %q; want %q", got.Bytes(), want)
	}

	wantErr := errors.New("writer failed")
	if err := WriteFramedRecords(failingWriter{err: wantErr}, records); !errors.Is(err, wantErr) {
		t.Fatalf("WriteFramedRecords() error = %v; want %v", err, wantErr)
	}
	if err := WriteFramedRecords(shortWriter{}, records); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("WriteFramedRecords() short-write error = %v; want %v", err, io.ErrShortWrite)
	}
}

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }

func BenchmarkFrameRecords(b *testing.B) {
	records := make([]WireRecord, 128)
	for i := range records {
		records[i] = WireRecord{Kind: KindFile, Display: "candidate", Payload: "Y2FuZGlkYXRl"}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		_ = FrameRecords(records)
	}
}

func BenchmarkEscapeDisplay(b *testing.B) {
	raw := []byte("printable\tunicode-\u00a0-invalid-\xff")
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		_ = EscapeDisplay(raw)
	}
}
