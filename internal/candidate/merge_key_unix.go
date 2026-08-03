//go:build !windows

package candidate

import "path/filepath"

func filesystemMergeKey(path string) string {
	return filepath.Clean(path)
}
