//go:build linux

package integration

import (
	"fmt"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func platformBenchmarkMetadata(binary string) (string, string, string) {
	var info unix.Statfs_t
	if err := unix.Statfs(filepath.Dir(binary), &info); err != nil {
		return "unavailable", "not-applicable", "not-applicable"
	}
	return fmt.Sprintf("linux-magic-0x%x", uint64(info.Type)), "not-applicable", "not-applicable"
}
