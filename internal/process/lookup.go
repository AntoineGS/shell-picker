package process

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

func lookPathInEnvironment(name string, environment []string) (string, error) {
	if filepath.IsAbs(name) || strings.ContainsAny(name, `/\`) {
		return exec.LookPath(name)
	}
	pathValue, found := environmentValue(environment, "PATH")
	if !found {
		return exec.LookPath(name)
	}
	for _, directory := range filepath.SplitList(pathValue) {
		candidate := filepath.Join(directory, name)
		if directory == "" {
			if runtime.GOOS == "windows" {
				candidate = `.\` + name
			} else {
				candidate = `./` + name
			}
		}
		if path, err := exec.LookPath(candidate); err == nil {
			return path, nil
		}
	}
	return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
}

func environmentValue(environment []string, name string) (string, bool) {
	for index := len(environment) - 1; index >= 0; index-- {
		entry, err := parseEnvironmentEntry(environment[index])
		if err != nil {
			continue
		}
		if sameEnvironmentName(entry.name, name, runtime.GOOS == "windows") {
			return entry.value, true
		}
	}
	return "", false
}
