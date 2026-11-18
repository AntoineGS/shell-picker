//go:build linux

package app

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/fsearchdb"
)

func appendUnearthRecord(dst *bytes.Buffer, path []byte) {
	_ = binary.Write(dst, binary.LittleEndian, uint32(len(path)))
	_, _ = dst.Write(path)
}

func TestDecodeUnearthRecordsPreservesRawPathBytesAndKinds(t *testing.T) {
	var stream bytes.Buffer
	appendUnearthRecord(&stream, []byte("/fixture/directory/"))
	appendUnearthRecord(&stream, []byte("/fixture/file-\xff\n.txt"))

	got, err := decodeUnearth(&stream)
	if err != nil {
		t.Fatal(err)
	}
	want := &fsearchdb.Database{Entries: []fsearchdb.Entry{
		{Path: []byte("/fixture/directory"), IsDir: true},
		{Path: []byte("/fixture/file-\xff\n.txt")},
	}}
	if len(got.Entries) != len(want.Entries) {
		t.Fatalf("got %d entries; want %d", len(got.Entries), len(want.Entries))
	}
	for i := range want.Entries {
		if !bytes.Equal(got.Entries[i].Path, want.Entries[i].Path) ||
			got.Entries[i].IsDir != want.Entries[i].IsDir {
			t.Errorf("entry %d = %#v; want %#v", i, got.Entries[i], want.Entries[i])
		}
	}
}

func TestDecodeUnearthRejectsMalformedRecordsWithoutPublishing(t *testing.T) {
	tests := []struct {
		name  string
		bytes []byte
	}{
		{name: "truncated size", bytes: []byte{1, 2, 3}},
		{name: "truncated body", bytes: []byte{4, 0, 0, 0, '/', 'x'}},
		{name: "zero length", bytes: []byte{0, 0, 0, 0}},
		{name: "relative", bytes: recordBytes([]byte("relative"))},
		{name: "nul", bytes: recordBytes([]byte("/fixture/a\x00b"))},
		{name: "too large", bytes: recordBytes(bytes.Repeat([]byte{'x'}, unearthMaxRecordBytes+1))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := decodeUnearth(bytes.NewReader(tt.bytes))
			if err == nil {
				t.Fatalf("decodeUnearth() = %#v; want error", got)
			}
			if got != nil {
				t.Fatalf("decodeUnearth() published partial database: %#v", got)
			}
		})
	}
}

func TestLoadUnearthUsesChildEnvironmentAndRejectsFailedProcess(t *testing.T) {
	bin := t.TempDir()
	path := filepath.Join(bin, "unearth")
	script := "#!/bin/sh\n" +
		"printf '\\003\\000\\000\\000/ok'\n" +
		"exit 7\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := loadUnearth(context.Background(), "/fixture", []string{"PATH=" + bin})
	if err == nil {
		t.Fatalf("loadUnearth() = %#v; want process failure", got)
	}
	if got != nil {
		t.Fatalf("loadUnearth() published successful-looking bytes from failed process: %#v", got)
	}
	if !strings.Contains(err.Error(), "unearth") {
		t.Fatalf("error = %v; want command context", err)
	}
}

func TestLoadUnearthPassesStrictLiveQueryArguments(t *testing.T) {
	bin := t.TempDir()
	streamPath := filepath.Join(bin, "records")
	if err := os.WriteFile(streamPath, recordBytes([]byte("/fixture/ok")), 0o600); err != nil {
		t.Fatal(err)
	}
	argsPath := filepath.Join(bin, "arguments")
	path := filepath.Join(bin, "unearth")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" > \"$ARGUMENT_LOG\"\n" +
		"exec /bin/cat \"$RECORDS\"\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := loadUnearth(context.Background(), "/fixture", []string{
		"PATH=" + bin, "ARGUMENT_LOG=" + argsPath, "RECORDS=" + streamPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantArgs := "--require-live-index\n--index-binary\n*\n--hidden\n/fixture\n"
	if arguments, err := os.ReadFile(argsPath); err != nil {
		t.Fatal(err)
	} else if string(arguments) != wantArgs {
		t.Fatalf("arguments = %q; want %q", arguments, wantArgs)
	}
	if len(got.Entries) != 1 || !bytes.Equal(got.Entries[0].Path, []byte("/fixture/ok")) {
		t.Fatalf("loaded entries = %#v", got.Entries)
	}
}

func TestLoadUnearthHonorsCancellation(t *testing.T) {
	bin := t.TempDir()
	path := filepath.Join(bin, "unearth")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nsleep 10\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := loadUnearth(ctx, "/fixture", []string{"PATH=" + bin})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("loadUnearth() error = %v; want context.Canceled", err)
	}
}

func TestLoadUnearthCancellationClosesInheritedOutputPipes(t *testing.T) {
	bin := t.TempDir()
	startedPath := filepath.Join(bin, "started")
	path := filepath.Join(bin, "unearth")
	script := "#!/bin/sh\n" +
		"/bin/sleep 30 &\n" +
		"printf started > \"$STARTED\"\n" +
		"printf '\\003\\000\\000\\000/ok'\n" +
		"exit 0\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	type result struct {
		database *fsearchdb.Database
		err      error
	}
	done := make(chan result, 1)
	go func() {
		database, err := loadUnearth(ctx, "/fixture", []string{
			"PATH=" + bin, "STARTED=" + startedPath,
		})
		done <- result{database: database, err: err}
	}()

	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(startedPath); err == nil {
			break
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			cancel()
			t.Fatal("unearth fixture did not start")
		}
	}
	cancel()

	select {
	case outcome := <-done:
		if !errors.Is(outcome.err, context.Canceled) {
			t.Fatalf("load error = %v; want context.Canceled", outcome.err)
		}
		if outcome.database != nil {
			t.Fatalf("canceled load published database: %#v", outcome.database)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("loadUnearth hung after cancellation with inherited stdout/stderr pipes")
	}
}

func recordBytes(path []byte) []byte {
	var stream bytes.Buffer
	appendUnearthRecord(&stream, path)
	return stream.Bytes()
}
