//go:build windows

package app

import "os"

func secureTraceFile(*os.File) error { return nil }
