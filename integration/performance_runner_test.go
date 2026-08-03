package integration

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"time"
)

func runPerformanceHelper() (int, bool) {
	if len(os.Args) == 3 && os.Args[1] == "query" && os.Args[2] == "--list" && os.Getenv("GO_PERF_HELPER") != "" {
		switch os.Getenv("GO_PERF_ZOXIDE_MODE") {
		case "timeout":
			signals := make(chan os.Signal, 1)
			signal.Notify(signals, os.Interrupt)
			<-signals
		case "empty":
		case "present", "":
			_, _ = fmt.Fprintln(os.Stdout, os.Getenv("GO_PERF_ZOXIDE_PATH"))
		case "blocked":
			connection, err := net.Dial("tcp", os.Getenv("GO_PERF_ZOXIDE_GATE"))
			if err != nil {
				return 3, true
			}
			var release [1]byte
			_, err = io.ReadFull(connection, release[:])
			_ = connection.Close()
			if err != nil {
				return 3, true
			}
		case "records-10000":
			root := os.Getenv("GO_PERF_ZOXIDE_PATH")
			for index := range 10_000 {
				_, _ = fmt.Fprintf(os.Stdout, "%s%cbench-%05d\n", root, os.PathSeparator, index)
			}
		default:
			return 2, true
		}
		return 0, true
	}
	switch os.Getenv("GO_PERF_HELPER") {
	case "fzf":
		data, _ := io.ReadAll(os.Stdin)
		if os.Getenv("GO_PERF_ZOXIDE_MODE") == "timeout" {
			time.Sleep(500 * time.Millisecond)
		}
		action := os.Getenv("GO_PERF_FZF_ACTION")
		if action != "" {
			start := time.Now().UnixNano()
			commandName := performanceCallbackName(os.Args[1:])
			commandArg := "e:up"
			if action == "preview" {
				commandArg = "p"
			}
			current := data
			if index := bytes.IndexByte(current, 0); index >= 0 {
				current = current[:index]
			}
			command := exec.Command(commandName, "--fzf-shell", commandArg)
			command.Env = replaceEnvironment(os.Environ(), "FZF_KEY=left", "FZF_QUERY=", "FZF_CURRENT_ITEM="+string(current))
			command.Stderr = io.Discard
			marker := performanceMarker{Start: start}
			if err := runPerformanceCallback(command, action, &marker); err != nil {
				return 3, true
			}
			encoded, _ := json.Marshal(marker)
			if err := os.WriteFile(os.Getenv("GO_PERF_MARKER"), encoded, 0o600); err != nil {
				return 4, true
			}
		}
		_, _ = os.Stdout.Write([]byte{0})
		return 130, true
	default:
		return 0, false
	}
}

func runPerformanceCallback(command *exec.Cmd, action string, marker *performanceMarker) error {
	if action != "event" {
		command.Stdout = io.Discard
		err := command.Run()
		marker.Reaped = time.Now().UnixNano()
		return err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return err
	}
	if err := command.Start(); err != nil {
		return err
	}
	buffer := make([]byte, 64<<10)
	written, readErr := stdout.Read(buffer)
	if written > 0 {
		marker.ActionWritten = time.Now().UnixNano()
	}
	_, drainErr := io.Copy(io.Discard, stdout)
	waitErr := command.Wait()
	marker.Reaped = time.Now().UnixNano()
	if written == 0 {
		return errors.Join(errors.New("callback wrote no action"), readErr, drainErr, waitErr)
	}
	return errors.Join(readErr, drainErr, waitErr)
}

func performanceCallbackName(arguments []string) string {
	for _, argument := range arguments {
		if value, ok := strings.CutPrefix(argument, "--with-shell="); ok {
			name, _, _ := strings.Cut(value, " ")
			return name
		}
	}
	return "shell-picker"
}
