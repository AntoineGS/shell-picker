//go:build windows

package process

import (
	"strings"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

func joinCommandLine(path string, args []string) string {
	parts := make([]string, 1, len(args)+1)
	parts[0] = windows.EscapeArg(path)
	for _, arg := range args {
		parts = append(parts, windows.EscapeArg(arg))
	}
	return strings.Join(parts, " ")
}

// BuildEnvironmentBlock validates and encodes a Windows environment block for
// callers that create a ConPTY process outside Runner.Start.
func BuildEnvironmentBlock(environment []string) ([]uint16, error) {
	if err := validateEnvironment(environment); err != nil {
		return nil, err
	}
	return buildEnvironmentBlock(environment), nil
}

func buildEnvironmentBlock(environment []string) []uint16 {
	parsed := make([]environmentEntry, 0, len(environment))
	for _, raw := range environment {
		entry, err := parseEnvironmentEntry(raw)
		if err != nil {
			return nil
		}
		parsed = append(parsed, entry)
	}
	sortEnvironmentEntries(parsed, true)
	block := make([]uint16, 0)
	for _, entry := range parsed {
		block = append(block, utf16.Encode([]rune(entry.raw))...)
		block = append(block, 0)
	}
	if len(block) == 0 {
		return []uint16{0, 0}
	}
	return append(block, 0)
}
