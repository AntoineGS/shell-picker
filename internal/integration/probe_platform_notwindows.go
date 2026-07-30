//go:build !windows

package integration

func detectWindowsCapabilities() ProbeWindows {
	return ProbeWindows{Job: "not-applicable", ConPTY: "not-applicable"}
}
