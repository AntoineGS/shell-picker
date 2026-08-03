package candidate

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	portableZoxideHelperEnv = "CANDIDATE_ZOXIDE_HELPER"
	portableZoxideModeEnv   = "CANDIDATE_ZOXIDE_MODE"
	portableZoxideScriptEnv = "CANDIDATE_ZOXIDE_SCRIPT"
)

func TestMain(m *testing.M) {
	if os.Getenv(portableZoxideHelperEnv) == "1" {
		switch os.Getenv(portableZoxideModeEnv) {
		case "ok":
			fmt.Fprintln(os.Stdout, filepath.Join(os.TempDir(), "shell-picker-zoxide-test"))
			os.Exit(0)
		case "multiple":
			fmt.Fprintln(os.Stdout, filepath.Join(os.TempDir(), "shell-picker-zoxide-same"))
			fmt.Fprintln(os.Stdout, filepath.Join(os.TempDir(), "shell-picker-zoxide-one"))
			fmt.Fprintln(os.Stdout, filepath.Join(os.TempDir(), "shell-picker-zoxide-two"))
			os.Exit(0)
		case "malformed":
			fmt.Fprintln(os.Stdout)
			fmt.Fprintln(os.Stdout, filepath.Join(os.TempDir(), "shell-picker-zoxide-test"))
			os.Exit(0)
		case "over-total":
			row := []byte(filepath.Join(string(filepath.Separator)+"z", "overflow") + "\n")
			for count := 0; count <= MaxZoxideOutputBytes/len(row); count++ {
				_, _ = os.Stdout.Write(row)
			}
			os.Exit(0)
		case "over-row":
			row := filepath.Join(string(filepath.Separator)+"z", strings.Repeat("x", MaxZoxideRowBytes)) + "\n"
			_, _ = fmt.Fprint(os.Stdout, row)
			os.Exit(0)
		case "over-rows":
			row := []byte(filepath.Join(string(filepath.Separator)+"z", "row") + "\n")
			for count := 0; count <= MaxZoxideRows; count++ {
				_, _ = os.Stdout.Write(row)
			}
			os.Exit(0)
		case "nonzero":
			fmt.Fprintln(os.Stdout, filepath.Join(os.TempDir(), "shell-picker-zoxide-test"))
			os.Exit(7)
		case "block":
			select {
			case <-time.After(24 * time.Hour):
			}
		case "script-file":
			script, err := os.ReadFile(os.Getenv(portableZoxideScriptEnv))
			if err != nil {
				os.Exit(3)
			}
			runPortableZoxideScript(string(script))
		}
	}
	os.Exit(m.Run())
}

func zoxideExecutable(t testing.TB, body string) (string, []string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		path, err := filepath.Abs(os.Args[0])
		if err != nil {
			t.Fatal(err)
		}
		script := filepath.Join(t.TempDir(), "zoxide-script.txt")
		if err := os.WriteFile(script, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return path, append(os.Environ(), portableZoxideHelperEnv+"=1", portableZoxideModeEnv+"=script-file", portableZoxideScriptEnv+"="+script)
	}
	dir := t.TempDir()
	name := "zoxide-test"
	path := filepath.Join(dir, name)
	body = "#!/bin/sh\nset -eu\n" + body
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return path, []string{"PATH=" + dir + string(os.PathListSeparator) + os.Getenv("PATH"), "FZF_DEFAULT_OPTS=blocked", "SHELL_PICKER_TEST=blocked"}
}

func runPortableZoxideScript(script string) {
	for _, rawLine := range strings.Split(script, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || line == "set -eu" || line == ":" {
			continue
		}
		if strings.HasPrefix(line, "test -z ") {
			for _, name := range []string{"FZF_DEFAULT_OPTS", "SHELL_PICKER_TEST"} {
				if strings.Contains(line, "${"+name+"+x}") {
					if _, exists := os.LookupEnv(name); exists {
						os.Exit(1)
					}
					break
				}
			}
			continue
		}
		if strings.HasPrefix(line, "printf ") {
			literal := strings.TrimSpace(strings.TrimPrefix(line, "printf "))
			if len(literal) < 2 || literal[0] != '\'' || literal[len(literal)-1] != '\'' {
				os.Exit(2)
			}
			_, _ = os.Stdout.Write(portableZoxideOutput(decodePortablePrintf(literal[1 : len(literal)-1])))
			continue
		}
		if strings.HasPrefix(line, "sleep ") {
			seconds, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimPrefix(line, "sleep ")), 64)
			if err != nil || seconds < 0 {
				os.Exit(2)
			}
			time.Sleep(time.Duration(seconds * float64(time.Second)))
			continue
		}
		if strings.HasPrefix(line, "exit ") {
			code, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "exit ")))
			if err != nil {
				os.Exit(2)
			}
			os.Exit(code)
		}
		os.Exit(2)
	}
	os.Exit(0)
}

func portableZoxidePath(path string) string {
	if runtime.GOOS != "windows" || !strings.HasPrefix(path, "/z/") {
		return path
	}
	return filepath.Join(os.TempDir(), "shell-picker-zoxide", strings.TrimPrefix(path, "/"))
}

func portableZoxideOutput(data []byte) []byte {
	if runtime.GOOS != "windows" {
		return data
	}
	result := bytes.Clone(data)
	for start := 0; start < len(result); {
		end := bytes.IndexByte(result[start:], '\n')
		if end < 0 {
			end = len(result) - start
		}
		rowEnd := start + end
		row := result[start:rowEnd]
		pathEnd := bytes.IndexByte(row, 0)
		if pathEnd < 0 {
			pathEnd = len(row)
		}
		if bytes.HasPrefix(row[:pathEnd], []byte("/z/")) {
			mapped := []byte(portableZoxidePath(string(row[:pathEnd])))
			row = append(mapped, row[pathEnd:]...)
			result = append(append(append([]byte(nil), result[:start]...), row...), result[rowEnd:]...)
			rowEnd = start + len(row)
		}
		start = rowEnd
		if start < len(result) && result[start] == '\n' {
			start++
		}
	}
	return result
}

func decodePortablePrintf(value string) []byte {
	decoded := make([]byte, 0, len(value))
	for index := 0; index < len(value); index++ {
		if value[index] != '\\' || index+1 >= len(value) {
			decoded = append(decoded, value[index])
			continue
		}
		index++
		switch value[index] {
		case 'n':
			decoded = append(decoded, '\n')
		case 'r':
			decoded = append(decoded, '\r')
		case 't':
			decoded = append(decoded, '\t')
		case '\\':
			decoded = append(decoded, '\\')
		default:
			if value[index] < '0' || value[index] > '7' {
				decoded = append(decoded, value[index])
				continue
			}
			digit := int(value[index] - '0')
			for count := 1; count < 3 && index+1 < len(value) && value[index+1] >= '0' && value[index+1] <= '7'; count++ {
				index++
				digit = digit*8 + int(value[index]-'0')
			}
			decoded = append(decoded, byte(digit))
		}
	}
	return decoded
}
