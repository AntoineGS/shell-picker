//go:build linux

package integration

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

func currentProcessIdentityMarker() (string, error) {
	return linuxProcessStartMarker(os.Getpid())
}

func verifyProcessIdentityMarker(identity ownedProcessIdentity, marker string) error {
	linuxIdentity, ok := identity.(*linuxProcessIdentity)
	if !ok {
		return errors.New("Linux process identity has unexpected implementation")
	}
	actual, err := linuxProcessStartMarker(linuxIdentity.pid)
	if err != nil {
		return err
	}
	if actual != marker {
		return fmt.Errorf("Linux process start marker=%q want %q: %w", actual, marker, errProcessIdentityChanged)
	}
	return nil
}

func linuxProcessStartMarker(pid int) (string, error) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return "", err
	}
	closing := strings.LastIndexByte(string(data), ')')
	if closing < 0 || closing+2 > len(data) {
		return "", errors.New("malformed Linux process stat")
	}
	fields := strings.Fields(string(data)[closing+2:])
	if len(fields) <= 19 {
		return "", errors.New("incomplete Linux process stat")
	}
	return fields[19], nil
}
