//go:build !windows

package pathutil

import (
	"bytes"
	"path/filepath"
	"strings"

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

func CompactHome(path, home []byte) []byte {
	if !filepath.IsAbs(string(path)) || !filepath.IsAbs(string(home)) {
		return bytes.Clone(path)
	}
	relative, err := filepath.Rel(string(home), string(path))
	if err != nil || relative == ".." || strings.HasPrefix(relative, "../") {
		return bytes.Clone(path)
	}
	if relative == "." {
		return []byte("~")
	}
	return append([]byte("~/"), relative...)
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
