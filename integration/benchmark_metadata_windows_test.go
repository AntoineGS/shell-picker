//go:build windows

package integration

import (
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

func platformBenchmarkMetadata(binary string) (string, string, string) {
	filesystem := "unavailable"
	path, err := windows.UTF16PtrFromString(filepath.Dir(binary))
	if err == nil {
		volume := make([]uint16, windows.MAX_PATH)
		if err = windows.GetVolumePathName(path, &volume[0], uint32(len(volume))); err == nil {
			name := make([]uint16, 64)
			if err = windows.GetVolumeInformation(&volume[0], nil, 0, nil, nil, nil, &name[0], uint32(len(name))); err == nil {
				filesystem = boundedMetadata(windows.UTF16ToString(name))
			}
		}
	}
	powerPlan := commandMetadata("powercfg", "/getactivescheme")
	defender := commandMetadata("sc.exe", "query", "WinDefend")
	if strings.Contains(strings.ToUpper(defender), "RUNNING") {
		defender = "running"
	} else if defender != "unavailable" {
		defender = "not-running"
	}
	return filesystem, powerPlan, defender
}

func commandMetadata(name string, arguments ...string) string {
	output, err := exec.Command(name, arguments...).Output()
	if err != nil {
		return "unavailable"
	}
	return boundedMetadata(strings.TrimSpace(string(output)))
}
