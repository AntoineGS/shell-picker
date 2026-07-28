//go:build windows

package pathutil

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/AntoineGS/shell-picker/internal/protocol"
)

var (
	kernel32          = syscall.NewLazyDLL("kernel32.dll")
	getLogicalDrives  = kernel32.NewProc("GetLogicalDrives")
	getFileAttributes = kernel32.NewProc("GetFileAttributesW")
)

func Root() Location {
	return Drives()
}

func Parent(location Location) Location {
	return parentWindows(location)
}

func parentWindows(location Location) Location {
	if location.Kind != KindFilesystem {
		return Drives()
	}
	clean := filepath.Clean(string(location.Path))
	volume := filepath.VolumeName(clean)
	if volume == "" || strings.EqualFold(clean, volume+`\`) || strings.EqualFold(clean, volume) {
		return Drives()
	}
	parent := filepath.Dir(clean)
	if strings.EqualFold(parent, volume) || strings.EqualFold(parent, volume+`.`) {
		return Drives()
	}
	return Filesystem([]byte(parent))
}

func Relative(base, target []byte) []byte {
	return relativeWindows(base, target)
}

func relativeWindows(base, target []byte) []byte {
	baseString := filepath.Clean(string(base))
	targetString := filepath.Clean(string(target))
	baseVolume := filepath.VolumeName(baseString)
	targetVolume := filepath.VolumeName(targetString)
	if !strings.EqualFold(baseVolume, targetVolume) {
		return bytes.Clone(target)
	}
	baseParts := windowsPathParts(baseString[len(baseVolume):])
	targetParts := windowsPathParts(targetString[len(targetVolume):])
	common := 0
	for common < len(baseParts) && common < len(targetParts) && strings.EqualFold(baseParts[common], targetParts[common]) {
		common++
	}
	parts := make([]string, 0, len(baseParts)-common+len(targetParts)-common)
	for range baseParts[common:] {
		parts = append(parts, "..")
	}
	parts = append(parts, targetParts[common:]...)
	result := "."
	if len(parts) > 0 {
		result = strings.Join(parts, `\`)
	}
	return []byte(result)
}

func PromptDisplay(location Location) string {
	if location.Kind != KindFilesystem {
		return `Drives\`
	}
	path := bytes.TrimRight(location.Path, `\/`)
	if len(path) == 0 {
		return `\`
	}
	var display strings.Builder
	start := 0
	for i, value := range path {
		if value != '\\' && value != '/' {
			continue
		}
		display.WriteString(protocol.EscapeDisplay(path[start:i]))
		display.WriteByte('\\')
		start = i + 1
	}
	display.WriteString(protocol.EscapeDisplay(path[start:]))
	display.WriteByte('\\')
	return display.String()
}

func ListDrives() ([]Location, error) {
	mask, _, callErr := getLogicalDrives.Call()
	if mask == 0 {
		if callErr == syscall.Errno(0) {
			callErr = syscall.EINVAL
		}
		return nil, fmt.Errorf("GetLogicalDrives: %w", callErr)
	}
	drives := make([]Location, 0, 26)
	for index := 0; index < 26; index++ {
		if mask&(uintptr(1)<<index) != 0 {
			drives = append(drives, Filesystem([]byte{byte('A' + index), ':', '\\'}))
		}
	}
	return drives, nil
}

func absoluteAncestryWindows(base string) ([]string, error) {
	if !filepath.IsAbs(base) {
		return nil, fmt.Errorf("base path %q is not absolute", base)
	}
	volume := filepath.VolumeName(base)
	if volume == "" || !validWindowsVolume(volume) {
		return nil, fmt.Errorf("base path %q has no volume", base)
	}
	root := volume + `\`
	ancestry := []string{root}
	current := root
	for _, component := range windowsPathParts(base[len(volume):]) {
		current = filepath.Join(current, component)
		ancestry = append(ancestry, current)
	}
	return ancestry, nil
}

func windowsPathParts(path string) []string {
	return strings.FieldsFunc(path, func(r rune) bool { return r == '\\' || r == '/' })
}

func isAbsolute(path []byte) bool {
	if len(path) > 0 && (path[0] == '\\' || path[0] == '/') {
		return true
	}
	return filepath.VolumeName(string(path)) != ""
}

func splitSeparators(path []byte) [][]byte {
	return bytes.FieldsFunc(path, func(r rune) bool { return r == '\\' || r == '/' })
}

func validWindowsVolume(volume string) bool {
	if !strings.HasPrefix(volume, `\\`) {
		return len(volume) == 2 && volume[1] == ':'
	}
	parts := windowsPathParts(volume)
	return len(parts) >= 2 && parts[0] != "" && parts[1] != ""
}
