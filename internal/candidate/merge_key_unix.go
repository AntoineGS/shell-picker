//go:build !windows

package candidate

import "path/filepath"

func filesystemRecordKey(path []byte) string {
	return filepath.Clean(string(path))
}
