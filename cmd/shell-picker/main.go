package main

import (
	"context"
	"os"

	"github.com/AntoineGS/shell-picker/internal/app"
)

var version = "dev"

func main() {
	os.Exit(app.Main(context.Background(), os.Args[1:], app.Streams{Out: os.Stdout, Err: os.Stderr}, version))
}
