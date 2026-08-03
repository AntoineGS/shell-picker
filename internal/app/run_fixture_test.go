package app

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/fzf"
	"github.com/AntoineGS/shell-picker/internal/process"
	"github.com/AntoineGS/shell-picker/internal/protocol"
	"github.com/AntoineGS/shell-picker/internal/sessionipc"
)

type pickerFixture struct {
	options      PickerOptions
	dependencies Dependencies
	cwd, child   string
	file         string
	tty          *os.File
}

func newPickerFixture(t *testing.T, picker protocol.Picker) pickerFixture {
	t.Helper()
	cwd := t.TempDir()
	child := filepath.Join(cwd, "child")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(cwd, "readme.md")
	if err := os.WriteFile(file, []byte("title\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tty, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tty.Close() })
	return pickerFixture{cwd: cwd, child: child, file: file, tty: tty,
		options: PickerOptions{Picker: picker, CWD: []byte(cwd), Home: []byte(cwd), Output: protocol.OutputNUL,
			FZFPath: "fzf", ExecutablePath: filepath.Join(cwd, "shell-picker"),
			ZoxidePolicy: candidate.ZoxideCached, ZoxideTimeout: candidate.DefaultZoxideTimeout()},
		dependencies: Dependencies{ProcessRunner: process.Runner{}, ZoxidePath: filepath.Join(cwd, "missing-zoxide"),
			Environment: []string{"PATH=/usr/bin", "SHELL_PICKER_TOKEN=forged"}, ForegroundTTY: tty}}
}

func recordForPath(t *testing.T, input io.ReadCloser, wanted string) []byte {
	t.Helper()
	return readFramedRecordsUntil(t, input, wanted)[wanted]
}

func readFramedRecord(input io.Reader) ([]byte, error) {
	var record bytes.Buffer
	buffer := []byte{0}
	for {
		n, err := input.Read(buffer)
		if n > 0 {
			if buffer[0] == 0 {
				return bytes.Clone(record.Bytes()), nil
			}
			if _, writeErr := record.Write(buffer[:n]); writeErr != nil {
				return nil, writeErr
			}
		}
		if err != nil {
			return nil, err
		}
	}
}

func readFramedRecordsUntil(t *testing.T, input io.Reader, wanted ...string) map[string][]byte {
	t.Helper()
	remaining := make(map[string]struct{}, len(wanted))
	for _, path := range wanted {
		remaining[path] = struct{}{}
	}
	records := make(map[string][]byte, len(remaining))
	for len(remaining) > 0 {
		raw, err := readFramedRecord(input)
		if err != nil {
			t.Fatalf("read framed record: %v", err)
		}
		wire, err := protocol.ParseRecord(raw)
		if err != nil {
			t.Fatal(err)
		}
		path, err := protocol.DecodePath(wire.Payload)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := remaining[string(path)]; ok {
			records[string(path)] = raw
			delete(remaining, string(path))
		}
	}
	return records
}

func recordForFramedPath(t *testing.T, framed []byte, wanted string) []byte {
	t.Helper()
	record, err := recordForFramedPathE(framed, wanted)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func recordForFramedPathE(framed []byte, wanted string) ([]byte, error) {
	for _, raw := range bytes.Split(bytes.TrimSuffix(framed, []byte{0}), []byte{0}) {
		wire, err := protocol.ParseRecord(raw)
		if err != nil {
			return nil, err
		}
		path, err := protocol.DecodePath(wire.Payload)
		if err != nil {
			return nil, err
		}
		if string(path) == wanted {
			return bytes.Clone(raw), nil
		}
	}
	return nil, fmt.Errorf("path %q absent from %q", wanted, framed)
}

func callbackClient(t *testing.T, config fzf.Config) *sessionipc.Client {
	t.Helper()
	values := map[string]string{"SHELL_PICKER_ADDR": config.CallbackAddress, "SHELL_PICKER_TOKEN": config.CallbackToken}
	client, err := sessionipc.NewClientFromEnv(func(key string) string { return values[key] })
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func finalizeTestEvent(t *testing.T, ctx context.Context, client *sessionipc.Client, response sessionipc.EventResponse, applied bool) {
	t.Helper()
	if response.EventID == 0 {
		return
	}
	if err := client.FinalizeEvent(ctx, sessionipc.EventFinalizeRequest{EventID: response.EventID, Applied: applied}); err != nil {
		t.Fatalf("FinalizeEvent(%d, applied=%t): %v", response.EventID, applied, err)
	}
}

func finalizeTestLoad(t *testing.T, ctx context.Context, client *sessionipc.Client, eventID uint64, applied bool) {
	t.Helper()
	if eventID == 0 {
		return
	}
	if err := client.FinalizeLoad(ctx, sessionipc.LoadFinalizeRequest{EventID: eventID, Applied: applied}); err != nil {
		t.Fatalf("FinalizeLoad(%d, applied=%t): %v", eventID, applied, err)
	}
}

type processCounts struct {
	mu                      sync.Mutex
	attempts, starts, exits int
	live, maxLive           int
}

func newProcessCounts() *processCounts { return &processCounts{} }
func (counts *processCounts) observe(event process.ProcessEvent) {
	counts.mu.Lock()
	defer counts.mu.Unlock()
	switch event.Phase {
	case "attempt":
		counts.attempts++
	case "start":
		counts.starts++
		counts.live++
		if counts.live > counts.maxLive {
			counts.maxLive = counts.live
		}
	case "exit":
		counts.live--
		counts.exits++
	}
}

func (counts *processCounts) lifecycleValues() (attempts, starts, maxLive, exits, live int) {
	counts.mu.Lock()
	defer counts.mu.Unlock()
	return counts.attempts, counts.starts, counts.maxLive, counts.exits, counts.live
}
func (counts *processCounts) values() (int, int, int) {
	counts.mu.Lock()
	defer counts.mu.Unlock()
	return counts.attempts, counts.starts, counts.maxLive
}

func zoxideFixture(t *testing.T, path string) string {
	t.Helper()
	executable := filepath.Join(t.TempDir(), "zoxide")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nprintf '%s\\n' '"+path+"'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return executable
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
