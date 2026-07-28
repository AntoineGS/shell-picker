//go:build !windows

package pathutil

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

func CreateDirectoryTree(base Location, query []byte) (CreatedTree, error) {
	if err := ValidateAddQuery(base, query); err != nil {
		return CreatedTree{}, err
	}
	rawBasePath := string(base.Path)
	ancestry, err := absoluteAncestryUnix(rawBasePath)
	if err != nil {
		return CreatedTree{}, err
	}
	for _, candidate := range ancestry {
		if err := checkDirectoryUnix(candidate); err != nil {
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
			if info.Mode()&os.ModeSymlink != 0 {
				return rollbackAfterError(&tree, fmt.Errorf("%w: %q is a symlink", ErrUnsafeTraversal, current))
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

func absoluteAncestryUnix(base string) ([]string, error) {
	if !filepath.IsAbs(base) {
		return nil, fmt.Errorf("base path %q is not absolute", base)
	}
	ancestry := []string{string(filepath.Separator)}
	current := string(filepath.Separator)
	for _, component := range splitSeparators([]byte(base)) {
		current = filepath.Join(current, string(component))
		ancestry = append(ancestry, current)
	}
	return ancestry, nil
}

func checkDirectoryUnix(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect base ancestry %q: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: base ancestor %q is a symlink", ErrUnsafeTraversal, path)
	}
	if !info.IsDir() {
		return fmt.Errorf("base ancestor %q is not a directory", path)
	}
	return nil
}

func isDirectoryNotEmpty(err error) bool {
	return errors.Is(err, syscall.ENOTEMPTY) || errors.Is(err, syscall.EEXIST)
}
