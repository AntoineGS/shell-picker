//go:build linux

package integration

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"
)

const (
	linuxProcessCommandLineMaxBytes = 64 << 10
	linuxProcessCommandLineMaxArgs  = 256
)

func parseLinuxProcessCommandLine(raw []byte) ([]string, error) {
	if len(raw) > linuxProcessCommandLineMaxBytes {
		return nil, fmt.Errorf("linux process command line exceeds %d bytes", linuxProcessCommandLineMaxBytes)
	}
	if len(raw) == 0 {
		return []string{}, nil
	}
	if raw[len(raw)-1] == 0 {
		raw = raw[:len(raw)-1]
	}
	if len(raw) == 0 {
		return []string{}, nil
	}
	parts := bytes.Split(raw, []byte{0})
	if len(parts) > linuxProcessCommandLineMaxArgs {
		return nil, fmt.Errorf("linux process command line has more than %d arguments", linuxProcessCommandLineMaxArgs)
	}
	args := make([]string, len(parts))
	for index, part := range parts {
		args[index] = string(part)
	}
	return args, nil
}

func readLinuxProcessCommandLine(pid int) ([]string, error) {
	file, err := os.Open(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, linuxProcessCommandLineMaxBytes+1))
	if err != nil {
		return nil, err
	}
	return parseLinuxProcessCommandLine(raw)
}

func TestParseLinuxProcessCommandLine(t *testing.T) {
	tests := []struct {
		name    string
		raw     []byte
		want    []string
		wantErr bool
	}{
		{name: "empty", raw: []byte{}, want: []string{}},
		{name: "nul separated", raw: []byte("/bin/picker\x00--listen=127.0.0.1:1\x00"), want: []string{"/bin/picker", "--listen=127.0.0.1:1"}},
		{name: "without trailing separator", raw: []byte("picker\x00arg"), want: []string{"picker", "arg"}},
		{name: "byte limit", raw: make([]byte, linuxProcessCommandLineMaxBytes+1), wantErr: true},
		{name: "argument count limit", raw: bytesForLinuxProcessArguments(linuxProcessCommandLineMaxArgs + 1), wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseLinuxProcessCommandLine(test.raw)
			if test.wantErr {
				if err == nil {
					t.Fatalf("parseLinuxProcessCommandLine(%d bytes) returned nil error", len(test.raw))
				}
				return
			}
			if err != nil {
				t.Fatalf("parseLinuxProcessCommandLine() error = %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("parseLinuxProcessCommandLine() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestReadLinuxProcessCommandLineReportsMissingProcess(t *testing.T) {
	if _, err := readLinuxProcessCommandLine(0); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("readLinuxProcessCommandLine(0) error = %v, want os.ErrNotExist", err)
	}
}

func TestLinuxSnapshotDescendantProcessRecordsReadsCurrentChild(t *testing.T) {
	const childEnvironment = "SHELL_PICKER_LINUX_PROC_FIXTURE_CHILD"
	if os.Getenv(childEnvironment) == "1" {
		_, _ = fmt.Fprintln(os.Stdout, "ready")
		select {}
	}

	readyReader, readyWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer readyReader.Close()
	child := exec.Command(os.Args[0], "-test.run", "^TestLinuxSnapshotDescendantProcessRecordsReadsCurrentChild$", "--", "task5-query-canary")
	child.Env = append(os.Environ(), childEnvironment+"=1")
	child.Stdout = readyWriter
	child.Stderr = io.Discard
	if err := child.Start(); err != nil {
		readyWriter.Close()
		t.Fatal(err)
	}
	readyWriter.Close()
	defer func() {
		_ = child.Process.Kill()
		_ = child.Wait()
	}()
	if ready, err := bufio.NewReader(readyReader).ReadString('\n'); err != nil || ready != "ready\n" {
		t.Fatalf("child readiness = %q, error = %v", ready, err)
	}

	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		records, snapshotErr := snapshotDescendantProcessRecords(os.Getpid())
		if snapshotErr != nil {
			t.Fatal(snapshotErr)
		}
		for _, record := range records {
			if record.PID != child.Process.Pid {
				continue
			}
			marker, markerErr := linuxProcessStartMarker(record.PID)
			if markerErr != nil {
				t.Fatal(markerErr)
			}
			if record.Identity != fmt.Sprintf("%d:%s", record.PID, marker) {
				t.Fatalf("child identity = %q, want PID/start marker %q", record.Identity, fmt.Sprintf("%d:%s", record.PID, marker))
			}
			wantArgs := []string{os.Args[0], "-test.run", "^TestLinuxSnapshotDescendantProcessRecordsReadsCurrentChild$", "--", "task5-query-canary"}
			if gotArgs := strings.Split(record.CommandLine, "\x00"); !reflect.DeepEqual(gotArgs, wantArgs) {
				t.Fatalf("child command args = %#v, want %#v", gotArgs, wantArgs)
			}
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("snapshot never observed child %d: records=%+v", child.Process.Pid, records)
		}
	}
}

func bytesForLinuxProcessArguments(count int) []byte {
	return []byte(strings.Repeat("arg\x00", count))
}
