//go:build windows

package integration

import (
	"errors"
	"fmt"
	"strconv"

	"golang.org/x/sys/windows"
)

func currentProcessIdentityMarker() (string, error) {
	creation, err := windowsProcessCreationTime(windows.CurrentProcess())
	if err != nil {
		return "", err
	}
	return strconv.FormatUint(creation, 10), nil
}

func verifyProcessIdentityMarker(identity ownedProcessIdentity, marker string) error {
	expected, err := strconv.ParseUint(marker, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid Windows process creation marker: %w", err)
	}
	windowsIdentity, ok := identity.(*windowsProcessIdentity)
	if !ok {
		return errors.New("Windows process identity has unexpected implementation")
	}
	actual, err := windowsProcessCreationTime(windowsIdentity.handle)
	if err != nil {
		return err
	}
	if actual != expected {
		return fmt.Errorf("Windows process creation marker=%d want %d: %w", actual, expected, errProcessIdentityChanged)
	}
	return nil
}

func windowsProcessCreationTime(handle windows.Handle) (uint64, error) {
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &creation, &exit, &kernel, &user); err != nil {
		return 0, err
	}
	return uint64(creation.HighDateTime)<<32 | uint64(creation.LowDateTime), nil
}
