//go:build windows

package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestBootstrapHandleProbe(t *testing.T) {
	var canary windows.Handle
	for _, argument := range os.Args[1:] {
		if strings.HasPrefix(argument, "canary=") {
			value, err := strconv.ParseUint(strings.TrimPrefix(argument, "canary="), 10, uintptrSize())
			if err != nil {
				t.Fatal(err)
			}
			canary = windows.Handle(value)
		}
	}
	if canary == 0 {
		return
	}
	fileType, err := windows.GetFileType(canary)
	if err != nil {
		fileType = windows.FILE_TYPE_UNKNOWN
	}
	_, _ = fmt.Fprintln(os.Stdout, fileType != windows.FILE_TYPE_UNKNOWN)
}

func TestBootstrapChildInheritsOnlyAllowListedHandles(t *testing.T) {
	security := &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), InheritHandle: 1}
	var inputRead, inputWrite windows.Handle
	err := windows.CreatePipe(&inputRead, &inputWrite, security, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(inputRead)
	defer windows.CloseHandle(inputWrite)
	var outputRead, outputWrite windows.Handle
	err = windows.CreatePipe(&outputRead, &outputWrite, security, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(outputRead)
	defer windows.CloseHandle(outputWrite)
	var canaryRead, canaryWrite windows.Handle
	err = windows.CreatePipe(&canaryRead, &canaryWrite, security, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(canaryRead)
	defer windows.CloseHandle(canaryWrite)

	information, err := startChild(os.Args[0], []string{"-test.run=^TestBootstrapHandleProbe$", "canary=" + strconv.FormatUint(uint64(canaryRead), 10)}, inputRead, outputWrite)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := windows.ResumeThread(information.Thread); err != nil {
		t.Fatal(err)
	}
	if _, err := windows.WaitForSingleObject(information.Process, windows.INFINITE); err != nil {
		t.Fatal(err)
	}
	if err := closeProcessInformation(&information); err != nil {
		t.Fatal(err)
	}
	_ = windows.CloseHandle(inputRead)
	_ = windows.CloseHandle(outputWrite)

	line, err := bufio.NewReader(os.NewFile(uintptr(outputRead), "probe-output")).ReadString('\n')
	if err != nil && err != io.EOF {
		t.Fatal(err)
	}
	if strings.TrimSpace(line) != "false" {
		t.Fatalf("canary inheritance result=%q, want false", line)
	}
}

func TestCloseReplacedStandardHandlesClosesOriginalsAndAllowsEOF(t *testing.T) {
	security := &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), InheritHandle: 1}
	var readHandle, writeHandle windows.Handle
	err := windows.CreatePipe(&readHandle, &writeHandle, security, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := closeReplacedStandardHandles([]windows.Handle{writeHandle, writeHandle}, windows.Handle(17), windows.Handle(18), windows.CloseHandle); err != nil {
		t.Fatal(err)
	}
	read := os.NewFile(uintptr(readHandle), "pipe-read")
	defer read.Close()
	result := make(chan error, 1)
	go func() {
		_, err := read.Read(make([]byte, 1))
		result <- err
	}()
	if err := <-result; err != io.EOF {
		t.Fatalf("read after replaced handle close = %v, want EOF", err)
	}
}

func uintptrSize() int {
	return strconv.IntSize
}
