package main

import (
	"os"

	"github.com/AntoineGS/shell-picker/internal/app"
)

var version = "dev"

func main() {
	ctx, cancel := signalContext()
	defer cancel()
	os.Exit(app.Main(ctx, os.Args[1:], app.Streams{Out: os.Stdout, Err: os.Stderr}, version))
}
