package process

import (
	"fmt"
	"runtime"
	"sort"
	"strings"
)

type environmentEntry struct {
	raw   string
	name  string
	value string
}

var blockedEnvironment = []string{
	"FZF_DEFAULT_OPTS", "FZF_DEFAULT_OPTS_FILE", "FZF_DEFAULT_COMMAND",
	"FZF_KEY", "FZF_QUERY", "FZF_CURRENT_ITEM",
}

func SanitizeEnv(inherited []string, controlled map[string]string) []string {
	windows := runtime.GOOS == "windows"
	controlledEntries := make([]environmentEntry, 0, len(controlled))
	for name, value := range controlled {
		entry, err := parseEnvironmentEntry(name + "=" + value)
		if err != nil || (entry.isDrivePseudo() && !windows) {
			continue
		}
		controlledEntries = append(controlledEntries, entry)
	}
	sortEnvironmentEntries(controlledEntries, windows)
	uniqueControlled := controlledEntries[:0]
	for _, entry := range controlledEntries {
		if len(uniqueControlled) > 0 && sameEnvironmentName(uniqueControlled[len(uniqueControlled)-1].name, entry.name, windows) {
			uniqueControlled[len(uniqueControlled)-1] = entry
			continue
		}
		uniqueControlled = append(uniqueControlled, entry)
	}
	controlledEntries = uniqueControlled

	last := make(map[string]int)
	if windows {
		for i, entry := range inherited {
			parsed, err := parseEnvironmentEntry(entry)
			if err == nil && (!parsed.isDrivePseudo() || windows) {
				last[environmentNameKey(parsed.name, windows)] = i
			}
		}
	}
	result := make([]string, 0, len(inherited)+len(controlled))
	for i, raw := range inherited {
		entry, err := parseEnvironmentEntry(raw)
		if err != nil || (entry.isDrivePseudo() && !windows) || blockedKey(entry.name, windows) || controlledKey(controlledEntries, entry.name, windows) {
			continue
		}
		if windows && last[environmentNameKey(entry.name, windows)] != i {
			continue
		}
		result = append(result, raw)
	}
	for _, entry := range controlledEntries {
		result = append(result, entry.raw)
	}
	return result
}

// parseEnvironmentEntry uses the first '=' for ordinary names and the next
// '=' for Windows drive pseudo-names such as =C:=C:\\path.
func parseEnvironmentEntry(entry string) (environmentEntry, error) {
	if entry == "" {
		return environmentEntry{}, fmt.Errorf("process: empty environment entry")
	}
	if strings.IndexByte(entry, 0) >= 0 {
		return environmentEntry{}, fmt.Errorf("process: environment entry contains NUL")
	}
	separator := strings.IndexByte(entry, '=')
	if separator < 0 {
		return environmentEntry{}, fmt.Errorf("process: environment entry has no equals sign: %q", entry)
	}
	if separator == 0 {
		relativeSeparator := strings.IndexByte(entry[1:], '=')
		if relativeSeparator < 0 {
			return environmentEntry{}, fmt.Errorf("process: invalid environment pseudo-key: %q", entry)
		}
		separator = relativeSeparator + 1
	}
	name, value := entry[:separator], entry[separator+1:]
	if name == "" {
		return environmentEntry{}, fmt.Errorf("process: empty environment name: %q", entry)
	}
	parsed := environmentEntry{raw: entry, name: name, value: value}
	if parsed.isDrivePseudo() && !validWindowsDriveEnvironmentEntry(parsed.name, parsed.value) {
		return environmentEntry{}, fmt.Errorf("process: invalid environment pseudo-key: %q", entry)
	}
	return parsed, nil
}

func validateEnvironment(environment []string) error {
	for _, entry := range environment {
		parsed, err := parseEnvironmentEntry(entry)
		if err != nil {
			return err
		}
		if parsed.isDrivePseudo() && runtime.GOOS != "windows" {
			return fmt.Errorf("process: invalid environment pseudo-key: %q", entry)
		}
	}
	return nil
}

func (entry environmentEntry) isDrivePseudo() bool {
	return len(entry.name) > 0 && entry.name[0] == '='
}

func validWindowsDriveEnvironmentEntry(name, value string) bool {
	if len(name) != 3 || name[0] != '=' || !asciiLetter(name[1]) || name[2] != ':' || len(value) < 3 {
		return false
	}
	return asciiEqualFold(value[0], name[1]) && value[1] == ':' && (value[2] == '\\' || value[2] == '/')
}

func asciiLetter(value byte) bool {
	return value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
}

func asciiEqualFold(left, right byte) bool {
	if left >= 'a' && left <= 'z' {
		left -= 'a' - 'A'
	}
	if right >= 'a' && right <= 'z' {
		right -= 'a' - 'A'
	}
	return left == right
}

func environmentNameKey(name string, windows bool) string {
	if windows {
		return strings.ToUpper(name)
	}
	return name
}

func sameEnvironmentName(left, right string, windows bool) bool {
	return environmentNameKey(left, windows) == environmentNameKey(right, windows)
}

// sortEnvironmentEntries orders parsed names case-insensitively on Windows,
// then uses exact name and raw entry ties for deterministic output.
func sortEnvironmentEntries(entries []environmentEntry, windows bool) {
	sort.SliceStable(entries, func(i, j int) bool {
		left, right := environmentNameKey(entries[i].name, windows), environmentNameKey(entries[j].name, windows)
		if left != right {
			return left < right
		}
		if entries[i].name != entries[j].name {
			return entries[i].name < entries[j].name
		}
		return entries[i].raw < entries[j].raw
	})
}

func blockedKey(key string, windows bool) bool {
	equal := func(a, b string) bool { return a == b }
	prefix := strings.HasPrefix(key, "SHELL_PICKER_")
	if windows {
		equal = strings.EqualFold
		prefix = len(key) >= len("SHELL_PICKER_") && strings.EqualFold(key[:len("SHELL_PICKER_")], "SHELL_PICKER_")
	}
	if prefix {
		return true
	}
	for _, blocked := range blockedEnvironment {
		if equal(key, blocked) {
			return true
		}
	}
	return false
}

func controlledKey(entries []environmentEntry, name string, windows bool) bool {
	for _, entry := range entries {
		if sameEnvironmentName(entry.name, name, windows) {
			return true
		}
	}
	return false
}
