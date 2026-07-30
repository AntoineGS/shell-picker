//go:build windows

package integration

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

func detectWindowsCapabilities() ProbeWindows {
	capabilities := ProbeWindows{Job: "unavailable", ConPTY: "unavailable"}
	job, err := windows.CreateJobObject(nil, nil)
	if err == nil {
		limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
		limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
		if _, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err == nil {
			capabilities.Job = "available"
		}
		_ = windows.CloseHandle(job)
	}
	if windows.RtlGetVersion().BuildNumber >= 17763 {
		capabilities.ConPTY = probeConPTY()
	}
	return capabilities
}

func probeConPTY() string {
	var inputRead, inputWrite, outputRead, outputWrite windows.Handle
	if err := windows.CreatePipe(&inputRead, &inputWrite, nil, 0); err != nil {
		return "unavailable"
	}
	defer windows.CloseHandle(inputRead)
	defer windows.CloseHandle(inputWrite)
	if err := windows.CreatePipe(&outputRead, &outputWrite, nil, 0); err != nil {
		return "unavailable"
	}
	defer windows.CloseHandle(outputRead)
	defer windows.CloseHandle(outputWrite)
	var console windows.Handle
	if err := windows.CreatePseudoConsole(windows.Coord{X: 80, Y: 25}, inputRead, outputWrite, 0, &console); err != nil {
		return "unavailable"
	}
	windows.ClosePseudoConsole(console)
	return "available"
}
