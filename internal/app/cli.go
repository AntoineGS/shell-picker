package app

import (
	"context"
	"fmt"
	"io"
)

type Streams struct {
	Out io.Writer
	Err io.Writer
}

func Main(_ context.Context, args []string, streams Streams, build string) int {
	if len(args) == 1 && args[0] == "version" {
		fmt.Fprintf(streams.Out, "shell-picker %s\n", Version(build))
		return 0
	}
	fmt.Fprintln(streams.Err, "usage: shell-picker version")
	return 2
}
