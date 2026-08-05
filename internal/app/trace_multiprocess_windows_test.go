//go:build windows

package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	integrationpkg "github.com/AntoineGS/shell-picker/internal/integration"
	"golang.org/x/sys/windows"
)

const (
	traceWriterHelperEnv     = "SHELL_PICKER_TRACE_WRITER_HELPER"
	traceWriterPathEnv       = "SHELL_PICKER_TRACE_WRITER_PATH"
	traceWriterIndexEnv      = "SHELL_PICKER_TRACE_WRITER_INDEX"
	traceWriterEventsEnv     = "SHELL_PICKER_TRACE_WRITER_EVENTS"
	traceWriterSessionEnv    = "SHELL_PICKER_TRACE_WRITER_SESSION"
	traceWriterGenerationEnv = "SHELL_PICKER_TRACE_WRITER_GENERATION"
	traceWriterWorkers       = 8
	traceWriterEvents        = 128
)

type traceHelperOutput struct {
	mu   sync.Mutex
	data bytes.Buffer
}

func (output *traceHelperOutput) Write(data []byte) (int, error) {
	output.mu.Lock()
	defer output.mu.Unlock()
	return output.data.Write(data)
}

func (output *traceHelperOutput) String() string {
	output.mu.Lock()
	defer output.mu.Unlock()
	return output.data.String()
}

func TestTraceWriterProcessHelper(t *testing.T) {
	if os.Getenv(traceWriterHelperEnv) != "1" {
		return
	}
	if sessionText := os.Getenv(traceWriterSessionEnv); sessionText != "" {
		session, err := decodeTraceWriterSession(sessionText)
		if err != nil {
			t.Fatal(err)
		}
		generation, err := strconv.ParseUint(os.Getenv(traceWriterGenerationEnv), 10, 64)
		if err != nil || generation == 0 {
			t.Fatalf("generation=%q err=%v", os.Getenv(traceWriterGenerationEnv), err)
		}
		sink, err := openTraceSink(os.Getenv(traceWriterPathEnv), session)
		if err != nil {
			t.Fatal(err)
		}
		trace := integrationpkg.NewTrace(sink, session)
		if err := trace.Event(integrationpkg.TraceEvent{Name: "generation.start", Generation: generation, Outcome: "ok"}); err != nil {
			t.Fatal(err)
		}
		if err := trace.Close(); err != nil {
			t.Fatal(err)
		}
		return
	}
	index, err := strconv.Atoi(os.Getenv(traceWriterIndexEnv))
	if err != nil {
		t.Fatal(err)
	}
	events, err := strconv.Atoi(os.Getenv(traceWriterEventsEnv))
	if err != nil {
		t.Fatal(err)
	}
	sink, err := openTraceSink(os.Getenv(traceWriterPathEnv), traceWriterSessionID(0))
	if err != nil {
		t.Fatal(err)
	}
	trace := integrationpkg.NewTrace(sink, traceWriterSessionID(0))
	for event := 0; event < events; event++ {
		generation := uint64(index*events + event + 1)
		if err := trace.Event(integrationpkg.TraceEvent{Name: "generation.start", Generation: generation, Outcome: "ok"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := trace.Close(); err != nil {
		t.Fatal(err)
	}
}

func testWindowsTraceSessionLifecycleProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.jsonl")
	sessionA := traceWriterSessionID(0)
	sessionB := traceWriterSessionID(1)
	if err := runSingleTraceWriterProcess(t, path, sessionA, 1); err != nil {
		t.Fatal(err)
	}
	assertTraceWriterGenerations(t, path, []uint64{1})
	if err := runSingleTraceWriterProcess(t, path, sessionA, 2); err != nil {
		t.Fatal(err)
	}
	assertTraceWriterGenerations(t, path, []uint64{1, 2})
	if err := runSingleTraceWriterProcess(t, path, sessionB, 3); err != nil {
		t.Fatal(err)
	}
	assertTraceWriterGenerations(t, path, []uint64{3})
}

func runSingleTraceWriterProcess(t *testing.T, path string, session [16]byte, generation uint64) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	environment := append([]string(nil), os.Environ()...)
	environment = append(environment, traceWriterHelperEnv+"=1", traceWriterPathEnv+"="+path,
		traceWriterSessionEnv+"="+hex.EncodeToString(session[:]), traceWriterGenerationEnv+"="+strconv.FormatUint(generation, 10))
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestTraceWriterProcessHelper$", "-test.v")
	command.Env = environment
	output := &traceHelperOutput{}
	command.Stdout = output
	command.Stderr = output
	if err := command.Run(); err != nil {
		return fmt.Errorf("single trace writer helper: %w; output=%q", err, output.String())
	}
	return ctx.Err()
}

func decodeTraceWriterSession(value string) ([16]byte, error) {
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != 16 {
		return [16]byte{}, fmt.Errorf("invalid trace writer session %q", value)
	}
	var session [16]byte
	copy(session[:], decoded)
	return session, nil
}

func assertTraceWriterGenerations(t *testing.T, path string, want []uint64) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != len(want) {
		t.Fatalf("trace records=%d want=%d data=%q", len(lines), len(want), data)
	}
	for index, line := range lines {
		record, err := integrationpkg.DecodeTraceRecordAt([]byte(line), time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		if record.Generation != want[index] {
			t.Fatalf("generation[%d]=%d want=%d", index, record.Generation, want[index])
		}
	}
}

func TestWindowsTraceWriterMultiProcessAtomicRecords(t *testing.T) {
	t.Run("file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "trace.jsonl")
		if err := os.WriteFile(path, []byte(`{"event":"stale"}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := runTraceWriterProcesses(t, path, nil); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		assertTraceWriterRecords(t, [][]byte{data})
	})

	t.Run("named pipe", func(t *testing.T) {
		// Each server instance is a separate byte stream. This validates complete
		// records on every pipe connection, not interleaving on one shared stream.
		path, readers := newTraceReaderPipes(t, traceWriterWorkers)
		var (
			dataMu      sync.Mutex
			data        = make([][]byte, 0, len(readers))
			readErr     = make(chan error, len(readers))
			readersWait sync.WaitGroup
		)
		for _, reader := range readers {
			readersWait.Add(1)
			go func(handle windows.Handle) {
				defer readersWait.Done()
				got, err := readTracePipe(handle)
				if err != nil {
					readErr <- err
					return
				}
				dataMu.Lock()
				data = append(data, got)
				dataMu.Unlock()
			}(reader)
		}
		if err := runTraceWriterProcesses(t, path, nil); err != nil {
			t.Fatal(err)
		}
		readersWait.Wait()
		close(readErr)
		for err := range readErr {
			t.Fatal(err)
		}
		dataMu.Lock()
		defer dataMu.Unlock()
		assertTraceWriterRecords(t, data)
	})
}

func runTraceWriterProcesses(t *testing.T, path string, extraEnv []string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	commands := make([]*exec.Cmd, 0, traceWriterWorkers)
	for index := 0; index < traceWriterWorkers; index++ {
		environment := append([]string(nil), os.Environ()...)
		environment = append(environment, traceWriterHelperEnv+"=1", traceWriterPathEnv+"="+path,
			traceWriterIndexEnv+"="+strconv.Itoa(index), traceWriterEventsEnv+"="+strconv.Itoa(traceWriterEvents))
		environment = append(environment, extraEnv...)
		command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestTraceWriterProcessHelper$", "-test.v")
		command.Env = environment
		output := &traceHelperOutput{}
		command.Stdout = output
		command.Stderr = output
		if err := command.Start(); err != nil {
			return err
		}
		commands = append(commands, command)
	}
	var wait sync.WaitGroup
	errorsCh := make(chan error, len(commands))
	for _, command := range commands {
		wait.Add(1)
		go func(command *exec.Cmd) {
			defer wait.Done()
			if err := command.Wait(); err != nil {
				stderr, _ := command.Stderr.(*traceHelperOutput)
				errorsCh <- fmt.Errorf("trace writer helper: %w; stderr=%q", err, stderr.String())
			}
		}(command)
	}
	wait.Wait()
	close(errorsCh)
	for err := range errorsCh {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func assertTraceWriterRecords(t *testing.T, chunks [][]byte) {
	t.Helper()
	want := traceWriterWorkers * traceWriterEvents
	seen := make(map[uint64]struct{}, want)
	count := 0
	for _, chunk := range chunks {
		if len(chunk) == 0 {
			continue
		}
		lines := strings.Split(string(chunk), "\n")
		for index, line := range lines {
			if line == "" {
				if index == len(lines)-1 {
					continue
				}
				t.Fatalf("blank trace record at chunk line %d", index)
			}
			record, err := integrationpkg.DecodeTraceRecordAt([]byte(line), time.Time{})
			if err != nil {
				t.Fatalf("trace record is not complete JSONL: %v; line=%q", err, line)
			}
			if record.Event != "generation.start" || record.Generation == 0 {
				t.Fatalf("unexpected trace record=%+v", record)
			}
			if _, exists := seen[record.Generation]; exists {
				t.Fatalf("duplicate generation=%d", record.Generation)
			}
			seen[record.Generation] = struct{}{}
			count++
		}
	}
	if count != want {
		t.Fatalf("trace record count=%d want=%d", count, want)
	}
}

func traceWriterSessionID(index int) [16]byte {
	var session [16]byte
	for offset := range session {
		session[offset] = byte('a' + (index+offset)%26)
	}
	return session
}

func newTraceReaderPipes(t *testing.T, count int) (string, []windows.Handle) {
	t.Helper()
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.Fatal(err)
	}
	path := `\\.\pipe\shell-picker-trace-test-` + hex.EncodeToString(raw[:])
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	readers := make([]windows.Handle, 0, count)
	for range count {
		handle, err := windows.CreateNamedPipe(name, windows.PIPE_ACCESS_INBOUND,
			windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT,
			uint32(count), 64<<10, 64<<10, 0, nil)
		if err != nil {
			for _, reader := range readers {
				_ = windows.CloseHandle(reader)
			}
			t.Fatal(err)
		}
		readers = append(readers, handle)
	}
	return path, readers
}

func readTracePipe(handle windows.Handle) ([]byte, error) {
	defer windows.CloseHandle(handle)
	if err := windows.ConnectNamedPipe(handle, nil); err != nil && !errors.Is(err, windows.ERROR_PIPE_CONNECTED) {
		return nil, err
	}
	data := make([]byte, 0, 32<<10)
	buffer := make([]byte, 32<<10)
	for {
		var read uint32
		err := windows.ReadFile(handle, buffer, &read, nil)
		if read > 0 {
			data = append(data, buffer[:read]...)
		}
		if errors.Is(err, windows.ERROR_BROKEN_PIPE) {
			return data, nil
		}
		if err != nil {
			return nil, err
		}
	}
}
