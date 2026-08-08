package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
)

func main() {
	if path := os.Getenv("CLINK_FAKE_ARGV"); path != "" {
		data, err := json.Marshal(os.Args[1:])
		if err != nil {
			panic(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			panic(err)
		}
	}

	if marker := os.Getenv("CLINK_FAKE_STDERR"); marker != "" {
		_, _ = fmt.Fprintln(os.Stderr, marker)
	}

	if output := os.Getenv("CLINK_FAKE_OUTPUT"); output != "" {
		file, err := os.Open(output)
		if err != nil {
			panic(err)
		}
		_, copyErr := io.Copy(os.Stdout, file)
		closeErr := file.Close()
		if copyErr != nil {
			panic(copyErr)
		}
		if closeErr != nil {
			panic(closeErr)
		}
	}

	if path := os.Getenv("CLINK_FAKE_EXIT_FILE"); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			panic(err)
		}
		value, err := strconv.Atoi(string(data))
		if err != nil {
			panic(err)
		}
		os.Exit(value)
	}
}
