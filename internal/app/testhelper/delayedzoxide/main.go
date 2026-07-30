package main

import (
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
)

func main() {
	counterPath, outputPath := os.Getenv("GO_TEST_COUNTER"), os.Getenv("GO_TEST_PATH")
	if counterPath == "" || outputPath == "" {
		os.Exit(2)
	}
	count := 0
	if raw, err := os.ReadFile(counterPath); err == nil {
		count, _ = strconv.Atoi(strings.TrimSpace(string(raw)))
	}
	count++
	if err := os.WriteFile(counterPath, []byte(strconv.Itoa(count)), 0o600); err != nil {
		os.Exit(3)
	}
	if count == 2 {
		interrupted := make(chan os.Signal, 1)
		signal.Notify(interrupted, os.Interrupt)
		<-interrupted
		return
	}
	if _, err := fmt.Fprintln(os.Stdout, outputPath); err != nil {
		os.Exit(4)
	}
}
