//go:build windows

package candidate

import (
	"path/filepath"
	"strings"
)

func filesystemMergeKey(path string) string {
	return strings.ToLower(filepath.Clean(filepath.FromSlash(path)))
}
