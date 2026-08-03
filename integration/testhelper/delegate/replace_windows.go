//go:build windows

package main

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var waitNamedPipeW = windows.NewLazySystemDLL("kernel32.dll").NewProc("WaitNamedPipeW")

type controlMessage struct {
	Event   string `json:"event"`
	Nonce   string `json:"nonce,omitempty"`
	PID     int    `json:"pid"`
	Columns int    `json:"columns,omitempty"`
	Lines   int    `json:"lines,omitempty"`
}

// Windows has no exec(2). The delegate therefore is the renderer process
// itself, preserving callback -> renderer -> helper-grandchild ancestry.
func replace(path string, arguments, environment []string) error {
	if len(arguments) < 4 {
		return errors.New("short delegate arguments")
	}
	mode, address, nonce := arguments[1], arguments[2], arguments[3]
	switch mode {
	case "fail":
		return errors.New("forced renderer failure")
	case "sentinel":
		return os.WriteFile(address, []byte("invoked"), 0o600)
	case "renderer", "overflow-renderer":
		return runRenderer(path, address, nonce, arguments[4:], environment, mode == "overflow-renderer")
	default:
		return errors.New("unknown delegate mode")
	}
}

func runRenderer(helper, address, nonce string, arguments, environment []string, overflow bool) error {
	connection, err := dialController(address)
	if err != nil {
		return err
	}
	defer connection.Close()
	columns, _ := strconv.Atoi(os.Getenv("FZF_PREVIEW_COLUMNS"))
	lines, _ := strconv.Atoi(os.Getenv("FZF_PREVIEW_LINES"))
	for index, argument := range arguments {
		if argument == "--size" && index+1 < len(arguments) {
			parts := strings.SplitN(arguments[index+1], "x", 2)
			if len(parts) == 2 {
				columns, _ = strconv.Atoi(parts[0])
				lines, _ = strconv.Atoi(parts[1])
			}
		}
	}
	if err := writeFrame(connection, controlMessage{Event: "renderer-started", Nonce: nonce, PID: os.Getpid(), Columns: columns, Lines: lines}); err != nil {
		return err
	}
	var ready controlMessage
	if err := readFrame(connection, &ready); err != nil {
		return err
	}
	if ready.Event != "controller-ready" || ready.Nonce != nonce {
		return errors.New("invalid controller readiness acknowledgement")
	}
	grandchild := exec.Command(helper, "grandchild", address, nonce)
	grandchild.Env = environment
	grandchild.Stdin, grandchild.Stdout, grandchild.Stderr = nil, io.Discard, io.Discard
	readyFile, err := os.CreateTemp("", "shell-picker-grandchild-ready-*")
	if err != nil {
		return err
	}
	readyPath := readyFile.Name()
	if err := readyFile.Close(); err != nil {
		_ = os.Remove(readyPath)
		return err
	}
	if err := os.Remove(readyPath); err != nil {
		return err
	}
	grandchild.Env = append(grandchild.Env, "SHELL_PICKER_GRANDCHILD_READY="+readyPath)
	defer os.Remove(readyPath)
	if err := grandchild.Start(); err != nil {
		return err
	}
	reaped := false
	defer func() {
		if !reaped {
			_ = grandchild.Process.Kill()
			_ = grandchild.Wait()
		}
	}()
	if err := waitGrandchildReady(readyPath); err != nil {
		return err
	}
	if overflow {
		var command controlMessage
		if err := readFrame(connection, &command); err != nil {
			return err
		}
		if command.Event != "start-overflow" || command.Nonce != nonce {
			return errors.New("invalid overflow start")
		}
		chunk := make([]byte, 64<<10)
		for range 80 {
			if _, err := os.Stdout.Write(chunk); err != nil {
				break
			}
		}
	}
	var command controlMessage
	if err := readFrame(connection, &command); err != nil {
		return err
	}
	if command.Event != "release" || command.Nonce != nonce {
		return errors.New("invalid release")
	}
	if err := grandchild.Process.Kill(); err != nil {
		return err
	}
	_ = grandchild.Wait()
	reaped = true
	return writeFrame(connection, controlMessage{Event: "renderer-exit", Nonce: nonce, PID: os.Getpid()})
}

func waitGrandchildReady(path string) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		data, err := os.ReadFile(path)
		if err == nil && len(data) != 0 {
			return nil
		}
		select {
		case <-ticker.C:
		case <-timer.C:
			return errors.New("grandchild readiness timeout")
		}
	}
}

func dialController(address string) (*os.File, error) {
	wide, err := windows.UTF16PtrFromString(address)
	if err != nil {
		return nil, err
	}
	if err := waitNamedPipe(wide, 30_000); err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(wide, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(handle), address), nil
}

func waitNamedPipe(name *uint16, timeout uint32) error {
	result, _, callErr := waitNamedPipeW.Call(uintptr(unsafe.Pointer(name)), uintptr(timeout))
	runtime.KeepAlive(name)
	if result != 0 {
		return nil
	}
	if callErr != syscall.Errno(0) {
		return callErr
	}
	return windows.ERROR_GEN_FAILURE
}

func writeFrame(writer io.Writer, value controlMessage) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err := binary.Write(writer, binary.BigEndian, uint32(len(payload))); err != nil {
		return err
	}
	_, err = writer.Write(payload)
	return err
}

func readFrame(reader io.Reader, value *controlMessage) error {
	var size uint32
	if err := binary.Read(reader, binary.BigEndian, &size); err != nil {
		return err
	}
	if size == 0 || size > 4096 {
		return io.ErrUnexpectedEOF
	}
	payload := make([]byte, size)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return err
	}
	return json.Unmarshal(payload, value)
}
