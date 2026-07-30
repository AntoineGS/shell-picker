package main

import (
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

type message struct {
	Event   string `json:"event"`
	PID     int    `json:"pid"`
	Columns int    `json:"columns,omitempty"`
	Lines   int    `json:"lines,omitempty"`
}

func main() {
	if len(os.Args) < 3 {
		os.Exit(2)
	}
	switch os.Args[1] {
	case "renderer":
		os.Exit(runRenderer(os.Args[2]))
	case "grandchild":
		os.Exit(runGrandchild(os.Args[2]))
	case "sentinel":
		if err := os.WriteFile(os.Args[2], []byte("invoked"), 0o600); err != nil {
			os.Exit(30)
		}
		os.Exit(0)
	default:
		os.Exit(2)
	}
}

func runRenderer(address string) int {
	connection, err := net.Dial("tcp4", address)
	if err != nil {
		return 10
	}
	defer connection.Close()
	columns, _ := strconv.Atoi(os.Getenv("FZF_PREVIEW_COLUMNS"))
	lines, _ := strconv.Atoi(os.Getenv("FZF_PREVIEW_LINES"))
	for index, argument := range os.Args[3:] {
		if argument == "--size" && index+4 < len(os.Args) {
			parts := strings.SplitN(os.Args[index+4], "x", 2)
			if len(parts) == 2 {
				columns, _ = strconv.Atoi(parts[0])
				lines, _ = strconv.Atoi(parts[1])
			}
		}
	}
	if err := writeFrame(connection, message{Event: "renderer-started", PID: os.Getpid(), Columns: columns, Lines: lines}); err != nil {
		return 11
	}
	grandchild := exec.Command(os.Args[0], "grandchild", address)
	grandchild.Stdin, grandchild.Stdout, grandchild.Stderr = nil, io.Discard, io.Discard
	if err := grandchild.Start(); err != nil {
		return 12
	}
	var command message
	if err := readFrame(connection, &command); err != nil || command.Event != "release" {
		_ = grandchild.Process.Kill()
		_ = grandchild.Wait()
		return 13
	}
	_ = grandchild.Process.Kill()
	_ = grandchild.Wait()
	_ = writeFrame(connection, message{Event: "renderer-exit", PID: os.Getpid()})
	return 0
}

func runGrandchild(address string) int {
	connection, err := net.Dial("tcp4", address)
	if err != nil {
		return 20
	}
	defer connection.Close()
	if err := writeFrame(connection, message{Event: "grandchild-started", PID: os.Getpid()}); err != nil {
		return 21
	}
	var command message
	if err := readFrame(connection, &command); err != nil {
		return 22
	}
	return 0
}

func writeFrame(writer io.Writer, value message) error {
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

func readFrame(reader io.Reader, destination *message) error {
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
	return json.Unmarshal(payload, destination)
}
