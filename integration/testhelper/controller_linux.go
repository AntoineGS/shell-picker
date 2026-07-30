//go:build !windows

package main

import (
	"io"
	"net"
)

func dialController(address string) (io.ReadWriteCloser, error) {
	return net.Dial("tcp4", address)
}
