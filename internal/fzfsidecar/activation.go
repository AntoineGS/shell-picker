package fzfsidecar

import (
	"runtime"
	"strings"
)

// ActivationVariable names the opt-in environment variable for the sidecar.
const ActivationVariable = "SHELL_PICKER_EXPERIMENTAL_FZF_SIDECAR"

// Enabled reports whether environment opts into the sidecar exactly.
func Enabled(environment []string) bool {
	windows := runtime.GOOS == "windows"
	found := false
	value := ""

	for _, entry := range environment {
		name, entryValue, ok := splitActivationEnvironmentEntry(entry, windows)
		if !ok {
			return false
		}

		matches := name == ActivationVariable
		if windows {
			matches = strings.EqualFold(name, ActivationVariable)
		}
		if !matches {
			continue
		}

		if !windows && found && value != entryValue {
			return false
		}
		found = true
		value = entryValue
	}

	return found && value == "1"
}

func splitActivationEnvironmentEntry(entry string, windows bool) (name, value string, ok bool) {
	if entry == "" || strings.IndexByte(entry, 0) >= 0 {
		return "", "", false
	}

	separator := strings.IndexByte(entry, '=')
	if separator < 0 {
		return "", "", false
	}
	if separator == 0 {
		if !windows {
			return "", "", false
		}
		relativeSeparator := strings.IndexByte(entry[1:], '=')
		if relativeSeparator < 0 {
			return "", "", false
		}
		separator = relativeSeparator + 1
		name, value = entry[:separator], entry[separator+1:]
		if !validActivationDriveEnvironmentEntry(name, value) {
			return "", "", false
		}
		return name, value, true
	}

	name, value = entry[:separator], entry[separator+1:]
	if name == "" {
		return "", "", false
	}
	return name, value, true
}

func validActivationDriveEnvironmentEntry(name, value string) bool {
	if name == "=::" {
		return value == "::\\"
	}
	if len(name) != 3 || name[0] != '=' || !activationASCIILetter(name[1]) || name[2] != ':' || len(value) < 3 {
		return false
	}
	return activationASCIIEqualFold(value[0], name[1]) && value[1] == ':' && (value[2] == '\\' || value[2] == '/')
}

func activationASCIILetter(value byte) bool {
	return value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
}

func activationASCIIEqualFold(left, right byte) bool {
	if left >= 'a' && left <= 'z' {
		left -= 'a' - 'A'
	}
	if right >= 'a' && right <= 'z' {
		right -= 'a' - 'A'
	}
	return left == right
}
