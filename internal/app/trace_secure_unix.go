//go:build !windows

package app

import "os"

func secureTraceFile(file *os.File) error { return file.Chmod(0o600) }
