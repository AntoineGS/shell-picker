package main

import (
	"fmt"
	"io"
	"net"
	"os"
	"time"
)

func main() {
	if len(os.Args) != 3 || os.Args[1] != "query" || os.Args[2] != "--list" {
		os.Exit(2)
	}
	switch os.Getenv("GO_PERF_ZOXIDE_MODE") {
	case "timeout":
		for {
			time.Sleep(time.Hour)
		}
	case "empty":
		return
	case "present", "":
		_, _ = fmt.Fprintln(os.Stdout, os.Getenv("GO_PERF_ZOXIDE_PATH"))
	case "blocked":
		connection, err := net.Dial("tcp", os.Getenv("GO_PERF_ZOXIDE_GATE"))
		if err != nil {
			os.Exit(3)
		}
		var release [1]byte
		_, err = io.ReadFull(connection, release[:])
		_ = connection.Close()
		if err != nil {
			os.Exit(3)
		}
	case "records-10000":
		root := os.Getenv("GO_PERF_ZOXIDE_PATH")
		for index := range 10_000 {
			_, _ = fmt.Fprintf(os.Stdout, "%s%cbench-%05d\n", root, os.PathSeparator, index)
		}
	default:
		os.Exit(2)
	}
}
