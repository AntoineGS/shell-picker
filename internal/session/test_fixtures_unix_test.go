//go:build !windows

package session

import (
	"time"

	"github.com/AntoineGS/shell-picker/internal/pathutil"
)

func sessionTestPath(path string) string { return path }

func sessionTestLocation(location pathutil.Location) pathutil.Location { return location }

func sessionTestRootPath() []byte { return []byte("/") }

func sessionTestDrivesHeader() string { return "" }

func sessionTestTransformDurationValid(duration time.Duration) bool { return duration > 0 }
