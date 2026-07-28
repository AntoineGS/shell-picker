//go:build !windows

package pathutil

import (
	"bytes"
	"path/filepath"

	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func Root() Location {
	return Filesystem([]byte{'/'})
}

func Parent(location Location) Location {
	if location.Kind != KindFilesystem {
		return Root()
	}
	return Filesystem([]byte(filepath.Dir(string(location.Path))))
}

func Relative(base, target []byte) []byte {
	relative, err := filepath.Rel(string(base), string(target))
	if err != nil {
		return bytes.Clone(target)
	}
	result := []byte(relative)
	if len(result) > 0 && result[0] == '-' {
		result = append([]byte("./"), result...)
	}
	return result
}

func PromptDisplay(location Location) string {
	if location.Kind != KindFilesystem {
		return "Drives/"
	}
	path := bytes.TrimRight(location.Path, "/")
	if len(path) == 0 {
		return "/"
	}
	return protocol.EscapeDisplay(path) + "/"
}

func isAbsolute(path []byte) bool {
	return filepath.IsAbs(string(path))
}

func splitSeparators(path []byte) [][]byte {
	return bytes.FieldsFunc(path, func(r rune) bool { return r == '/' })
}
