//go:build windows

package process

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	winCreateJobObject          = windows.CreateJobObject
	winSetInformationJobObject  = windows.SetInformationJobObject
	winAssignProcessToJobObject = windows.AssignProcessToJobObject
	winTerminateJobObject       = windows.TerminateJobObject
)

func createKillJob() (windows.Handle, error) {
	job, err := winCreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	_, err = winSetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits)))
	if err != nil {
		_ = winCloseHandle(job)
		return 0, err
	}
	return job, nil
}

func terminateJob(job windows.Handle) error {
	err := winTerminateJobObject(job, 1)
	if err == windows.ERROR_ACCESS_DENIED {
		return nil
	}
	return err
}
