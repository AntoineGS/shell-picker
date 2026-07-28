//go:build windows

package pathutil

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

const (
	invalidFileAttributes     = 0xffffffff
	fileAttributeReparsePoint = 0x00000400
	errorDirectoryNotEmpty    = syscall.Errno(145)
)

func CreateDirectoryTree(base Location, query []byte) (CreatedTree, error) {
	if err := ValidateAddQuery(base, query); err != nil {
		return CreatedTree{}, err
	}
	rawBasePath := string(base.Path)
	ancestry, err := absoluteAncestryWindows(rawBasePath)
	if err != nil {
		return CreatedTree{}, err
	}
	for _, candidate := range ancestry {
		if err := checkDirectoryWindows(candidate); err != nil {
			return CreatedTree{}, err
		}
	}

	basePath := filepath.Clean(rawBasePath)
	tree := CreatedTree{Target: Filesystem([]byte(basePath))}
	current := basePath
	for _, component := range splitSeparators(query) {
		if string(component) == "." {
			continue
		}
		current = filepath.Join(current, string(component))
		info, statErr := os.Lstat(current)
		switch {
		case statErr == nil:
			if err := rejectReparsePoint(current); err != nil {
				return rollbackAfterError(&tree, err)
			}
			if !info.IsDir() {
				return rollbackAfterError(&tree, fmt.Errorf("path component %q is not a directory", current))
			}
		case errors.Is(statErr, fs.ErrNotExist):
			// Checks and creation cannot be atomic against concurrent namespace
			// replacement; callers must treat this as the unavoidable TOCTOU boundary.
			if mkdirErr := os.Mkdir(current, 0o777); mkdirErr != nil {
				return rollbackAfterError(&tree, fmt.Errorf("create directory %q: %w", current, mkdirErr))
			}
			tree.Created = append(tree.Created, []byte(current))
		default:
			return rollbackAfterError(&tree, fmt.Errorf("inspect path component %q: %w", current, statErr))
		}
	}
	tree.Target = Filesystem([]byte(current))
	return tree, nil
}

func checkDirectoryWindows(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect base ancestry %q: %w", path, err)
	}
	if err := rejectReparsePoint(path); err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("base ancestor %q is not a directory", path)
	}
	return nil
}

func rejectReparsePoint(path string) error {
	pointer, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return fmt.Errorf("encode path %q: %w", path, err)
	}
	attributes, _, callErr := getFileAttributes.Call(uintptr(unsafe.Pointer(pointer)))
	if uint32(attributes) == invalidFileAttributes {
		return fmt.Errorf("GetFileAttributesW %q: %w", path, callErr)
	}
	if uint32(attributes)&fileAttributeReparsePoint != 0 {
		return fmt.Errorf("%w: %q is a reparse point", ErrUnsafeTraversal, path)
	}
	return nil
}

func isDirectoryNotEmpty(err error) bool {
	return errors.Is(err, errorDirectoryNotEmpty) || errors.Is(err, syscall.ENOTEMPTY) || errors.Is(err, syscall.EEXIST)
}
