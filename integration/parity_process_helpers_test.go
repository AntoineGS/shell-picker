package integration

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func runParityHelper() int {
	mode := os.Getenv(parityHelperEnvironment)
	switch mode {
	case "zoxide-ok":
		root := os.Getenv("PARITY_TEST_ROOT")
		_, _ = fmt.Fprintf(os.Stdout, "%s\n%s\n%s\n", filepath.Join(root, "visible"), filepath.Join(root, "zoxide-one"), filepath.Join(root, "zoxide-two"))
		return 0
	case "zoxide-fail":
		_, _ = io.WriteString(os.Stdout, "partial")
		return 7
	case "zoxide-malformed":
		_, _ = io.WriteString(os.Stdout, "relative\n")
		return 0
	case "tool-success", "tool-fail", "tool-hang", "tool-overflow", "tool-descendant":
		logParityToolProcess(mode)
		if mode == "tool-fail" {
			return 7
		}
		if mode == "tool-descendant" {
			for {
				time.Sleep(time.Hour)
			}
		}
		if mode == "tool-hang" {
			child := exec.Command(os.Args[0])
			child.Env = replaceEnvironment(os.Environ(), parityHelperEnvironment+"=tool-descendant")
			if err := child.Start(); err != nil {
				return 124
			}
			return exitCode(child.Wait())
		}
		if mode == "tool-overflow" {
			_, _ = os.Stdout.Write(bytes.Repeat([]byte("overflow"), 1024))
			return 0
		}
		tool := filepath.Base(os.Args[0])
		switch tool {
		case "pdftoppm":
			return writeParityArtifact(os.Args[len(os.Args)-1] + ".jpg")
		case "ffmpegthumbnailer":
			for index, argument := range os.Args {
				if argument == "-o" && index+1 < len(os.Args) {
					return writeParityArtifact(os.Args[index+1])
				}
			}
			return 123
		case "ffmpeg":
			return writeParityArtifact(os.Args[len(os.Args)-1])
		case "file":
			_, _ = io.WriteString(os.Stdout, "application/octet-stream\n")
		default:
			_, _ = io.WriteString(os.Stdout, "external preview\n")
		}
		return 0
	case "authority-tool":
		return runAuthorityTool(filepath.Base(os.Args[0]), os.Args[1:])
	default:
		return 125
	}
}

func runAuthorityTool(tool string, arguments []string) int {
	switch tool {
	case "base64":
		raw, err := io.ReadAll(os.Stdin)
		if err != nil {
			return 120
		}
		decode := false
		for _, argument := range arguments {
			decode = decode || argument == "--decode"
		}
		if decode {
			decoded, err := base64.StdEncoding.DecodeString(string(raw))
			if err != nil {
				return 1
			}
			_, err = os.Stdout.Write(decoded)
		} else {
			_, err = io.WriteString(os.Stdout, base64.StdEncoding.EncodeToString(raw))
		}
		if err != nil {
			return 121
		}
		return 0
	case "fd":
		base := ""
		directoriesOnly := false
		for index, argument := range arguments {
			if argument == "--base-directory" && index+1 < len(arguments) {
				base = arguments[index+1]
			}
			directoriesOnly = directoriesOnly || argument == "d" && index > 0 && arguments[index-1] == "--type"
		}
		entries, err := os.ReadDir(base)
		if err != nil {
			return 1
		}
		for _, entry := range entries {
			if directoriesOnly && !entry.IsDir() {
				continue
			}
			if _, err := os.Stdout.Write(append([]byte("./"+entry.Name()), 0)); err != nil {
				return 122
			}
		}
		return 0
	case "realpath":
		base, target, zero := "", "", false
		for _, argument := range arguments {
			if strings.HasPrefix(argument, "--relative-to=") {
				base = strings.TrimPrefix(argument, "--relative-to=")
			} else if argument == "--zero" {
				zero = true
			} else if !strings.HasPrefix(argument, "-") {
				target = argument
			}
		}
		parent, err := os.Stat(filepath.Dir(target))
		if err != nil || !parent.IsDir() {
			return 1
		}
		relative, err := filepath.Rel(base, target)
		if err != nil {
			return 1
		}
		terminator := byte('\n')
		if zero {
			terminator = 0
		}
		_, err = os.Stdout.Write(append([]byte(relative), terminator))
		if err != nil {
			return 123
		}
		return 0
	case "file":
		if len(arguments) == 0 {
			return 1
		}
		path := arguments[len(arguments)-1]
		info, err := os.Stat(path)
		if err != nil {
			return 1
		}
		mime := false
		for _, argument := range arguments {
			mime = mime || argument == "--mime-type"
		}
		value := "ASCII text"
		if mime {
			value = "text/plain"
		}
		if info.IsDir() {
			value = "inode/directory"
		}
		_, err = fmt.Fprintln(os.Stdout, value)
		if err != nil {
			return 124
		}
		return 0
	case "bat":
		if len(arguments) == 0 {
			return 1
		}
		path := arguments[len(arguments)-1]
		raw, err := os.ReadFile(path)
		if err != nil {
			return 1
		}
		if _, err := os.Stdout.Write(raw); err != nil {
			return 125
		}
		return 0
	default:
		return 126
	}
}

func logParityToolProcess(mode string) {
	path := os.Getenv("PARITY_TEST_TOOL_LOG")
	if path == "" {
		return
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_ = json.NewEncoder(file).Encode(struct {
		Mode string   `json:"mode"`
		PID  int      `json:"pid"`
		Args []string `json:"args"`
	}{Mode: mode, PID: os.Getpid(), Args: os.Args})
	_ = file.Close()
}

func writeParityArtifact(path string) int {
	if err := os.WriteFile(path, []byte("\x89PNG\r\n\x1a\nparity"), 0o600); err != nil {
		return 122
	}
	return 0
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return 121
}
