//go:build linux

package integration

import (
	"bytes"
	"fmt"
	"os"
	"testing"
)

func TestLinuxTerminalSessionSeparatesResultsFromScreen(t *testing.T) {
	term := newTerminalSession(t, terminalConfig{
		Path: os.Args[0], Args: []string{"-test.run=^TestLinuxTerminalResultHelperProcess$", "--"},
		Environment: append(os.Environ(), "SHELL_PICKER_TERMINAL_HELPER=results"), Columns: 80, Lines: 24,
	})
	t.Cleanup(func() {
		if err := term.Close(); err != nil {
			t.Errorf("close terminal: %v", err)
		}
	})
	if err := term.Wait(testContext(t)); err != nil {
		t.Fatal(err)
	}
	if got, want := term.ResultBytes(), []byte("selected\nwith-newline\x00"); !bytes.Equal(got, want) {
		t.Fatalf("result bytes=%q; want exact stdout %q", got, want)
	}
	if got := term.Output(); !bytes.Contains(got, []byte("SCREEN-BYTES")) || bytes.Contains(got, []byte("selected")) {
		t.Fatalf("terminal bytes=%q; want screen output only", got)
	}
}

func TestLinuxTerminalResultHelperProcess(t *testing.T) {
	if os.Getenv("SHELL_PICKER_TERMINAL_HELPER") != "results" {
		return
	}
	terminal, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
	if err != nil {
		os.Exit(2)
	}
	_, terminalErr := fmt.Fprintln(terminal, "SCREEN-BYTES")
	closeErr := terminal.Close()
	_, resultErr := os.Stdout.Write([]byte("selected\nwith-newline\x00"))
	if terminalErr != nil || closeErr != nil || resultErr != nil {
		os.Exit(3)
	}
	os.Exit(0)
}

func TestLinuxTerminalSessionCanDisablePickerTrace(t *testing.T) {
	term := newTerminalSession(t, terminalConfig{
		Path: os.Args[0], Args: []string{"-test.run=^TestLinuxTerminalResultHelperProcess$"},
		Environment: append(os.Environ(), "SHELL_PICKER_TERMINAL_HELPER=results"), Columns: 80, Lines: 24,
		DisablePickerTrace: true,
	})
	t.Cleanup(func() {
		if err := term.Close(); err != nil {
			t.Errorf("close terminal: %v", err)
		}
	})
	if err := term.Wait(testContext(t)); err != nil {
		t.Fatalf("trace-disabled helper wait: %v; screen=%q", err, term.Output())
	}
	if got := term.ResultBytes(); !bytes.Equal(got, []byte("selected\nwith-newline\x00")) {
		t.Fatalf("trace-disabled helper result=%q", got)
	}
}
