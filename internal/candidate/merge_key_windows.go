//go:build windows

package candidate

import (
	"path/filepath"
	"strings"
)

func filesystemRecordKey(path []byte) string {
	// Construct the normalized string directly from bytes. Converting to a
	// string before FromSlash allocates both the input and the replaced path.
	var normalized strings.Builder
	normalized.Grow(len(path))
	for _, value := range path {
		if value == '/' {
			value = '\\'
		}
		normalized.WriteByte(value)
	}
	return strings.ToLower(filepath.Clean(normalized.String()))
}
