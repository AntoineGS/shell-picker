package main

import (
	"context"
	"io"
	"os"

	"github.com/AntoineGS/shell-picker/internal/app"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--fzf-shell" {
		os.Exit(runCallback(os.Args[1:]))
	}
	os.Exit(app.Main(context.Background(), os.Args[1:], app.Streams{Out: os.Stdout, Err: os.Stderr}, "task5-recording"))
}

func runCallback(args []string) int {
	stdout, stdoutFile, err := captureWriter(os.Getenv("SHELL_PICKER_TASK5_CALLBACK_OUTPUT"), os.Stdout)
	if err != nil {
		return 2
	}
	defer closeCapture(stdoutFile)
	stderr, stderrFile, err := captureWriter(os.Getenv("SHELL_PICKER_TASK5_CALLBACK_STDERR"), os.Stderr)
	if err != nil {
		return 2
	}
	defer closeCapture(stderrFile)
	return app.Main(context.Background(), args, app.Streams{Out: stdout, Err: stderr}, "task5-recording")
}

func captureWriter(path string, terminal io.Writer) (io.Writer, *os.File, error) {
	if path == "" {
		return terminal, nil, nil
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, nil, err
	}
	return io.MultiWriter(terminal, file), file, nil
}

func closeCapture(file *os.File) {
	if file != nil {
		_ = file.Close()
	}
}
