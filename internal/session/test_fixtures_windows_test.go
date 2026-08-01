//go:build windows

package session

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/AntoineGS/shell-picker/internal/pathutil"
)

func sessionTestPath(path string) string {
	if path == "/" {
		return `C:\`
	}
	if strings.HasPrefix(path, "/") {
		return filepath.Join(`C:\`, strings.TrimPrefix(path, "/"))
	}
	return path
}

func sessionTestLocation(location pathutil.Location) pathutil.Location {
	if location.Kind != pathutil.KindFilesystem {
		return location
	}
	return pathutil.Filesystem([]byte(sessionTestPath(string(location.Path))))
}

func sessionTestRootPath() []byte { return nil }

func sessionTestDrivesHeader() string { return pathutil.PromptDisplay(pathutil.Drives()) }

func sessionTestTransformDurationValid(duration time.Duration) bool { return duration >= 0 }
