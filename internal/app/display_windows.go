package app

import "os"

// Native Windows fzf uses the full console; height mode requires a Unix TTY.
func pickerHeight(_ string, _ *os.File) string { return "" }
