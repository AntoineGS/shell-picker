//go:build !linux && !windows

package integration

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

func currentProcessIdentityMarker() (string, error) {
	return "pid:" + strconv.Itoa(os.Getpid()), nil
}

func verifyProcessIdentityMarker(identity ownedProcessIdentity, marker string) error {
	want := "pid:" + strconv.Itoa(identity.PID())
	if strings.TrimSpace(marker) != want {
		return fmt.Errorf("process marker=%q want %q", marker, want)
	}
	return nil
}
