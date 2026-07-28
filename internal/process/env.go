package process

import (
	"runtime"
	"sort"
	"strings"
)

var blockedEnvironment = []string{
	"FZF_DEFAULT_OPTS", "FZF_DEFAULT_OPTS_FILE", "FZF_DEFAULT_COMMAND",
	"FZF_KEY", "FZF_QUERY", "FZF_CURRENT_ITEM",
}

func SanitizeEnv(inherited []string, controlled map[string]string) []string {
	windows := runtime.GOOS == "windows"
	controlledKeys := make([]string, 0, len(controlled))
	for key := range controlled {
		controlledKeys = append(controlledKeys, key)
	}
	if windows {
		sort.Slice(controlledKeys, func(i, j int) bool {
			left, right := strings.ToUpper(controlledKeys[i]), strings.ToUpper(controlledKeys[j])
			if left == right {
				return controlledKeys[i] < controlledKeys[j]
			}
			return left < right
		})
		unique := controlledKeys[:0]
		for _, key := range controlledKeys {
			if len(unique) > 0 && strings.EqualFold(unique[len(unique)-1], key) {
				unique[len(unique)-1] = key
				continue
			}
			unique = append(unique, key)
		}
		controlledKeys = unique
	} else {
		sort.Strings(controlledKeys)
	}
	last := make(map[string]int)
	if windows {
		for i, entry := range inherited {
			key, _, ok := strings.Cut(entry, "=")
			if ok {
				last[strings.ToUpper(key)] = i
			}
		}
	}
	result := make([]string, 0, len(inherited)+len(controlled))
	for i, entry := range inherited {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || blockedKey(key, windows) || controlledKey(controlledKeys, key, windows) {
			continue
		}
		if windows && last[strings.ToUpper(key)] != i {
			continue
		}
		result = append(result, entry)
	}
	for _, key := range controlledKeys {
		result = append(result, key+"="+controlled[key])
	}
	return result
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

func controlledKey(keys []string, key string, windows bool) bool {
	for _, candidate := range keys {
		if candidate == key || windows && strings.EqualFold(candidate, key) {
			return true
		}
	}
	return false
}
