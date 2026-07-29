//go:build windows

package process

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	winNewAttributeList = windows.NewProcThreadAttributeList
	winUpdateHandleList = func(list *windows.ProcThreadAttributeListContainer, handles []windows.Handle) error {
		return list.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST, unsafe.Pointer(&handles[0]),
			uintptr(len(handles))*unsafe.Sizeof(handles[0]))
	}
	winCreateProcess       = windows.CreateProcess
	winResumeThread        = windows.ResumeThread
	winTerminateProcess    = windows.TerminateProcess
	winWaitForSingleObject = windows.WaitForSingleObject
	winGetExitCodeProcess  = windows.GetExitCodeProcess
)

type processResult struct {
	code uint32
	err  error
}

type Child struct {
	process     windows.Handle
	job         windows.Handle
	containment Containment
	pid         int
	ctx         context.Context
	path        string
	observe     func(ProcessEvent)
	streams     *preparedStreams

	waitOnce      sync.Once
	waitErr       error
	lifeMu        sync.Mutex
	processExited bool
	targetValid   bool
	killIssued    bool
	killErr       error
	cancelErr     error
	observedExit  chan struct{}
	result        chan processResult
	watchDone     chan struct{}
	pumpDone      chan struct{}
	pumpErr       chan error
}

func (r Runner) Start(ctx context.Context, spec Spec) (*Child, error) {
	if err := validateSpec(ctx, spec); err != nil {
		return nil, err
	}
	observe(r.Observe, "attempt", spec.Path, 0)
	path, err := exec.LookPath(spec.Path)
	if err != nil {
		if errors.Is(err, syscall.ENOENT) || errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
			return nil, fmt.Errorf("%w: %v", exec.ErrNotFound, err)
		}
		return nil, err
	}
	streams, err := prepareStreams(spec)
	if err != nil {
		return nil, err
	}
	job, err := createKillJob()
	if err != nil {
		streams.closeAll()
		return nil, err
	}
	attributes, err := winNewAttributeList(1)
	if err != nil {
		streams.closeAll()
		_ = winCloseHandle(job)
		return nil, err
	}
	defer attributes.Delete()
	if err := winUpdateHandleList(attributes, streams.children); err != nil {
		streams.closeAll()
		_ = winCloseHandle(job)
		return nil, err
	}
	info, err := createSuspendedProcess(path, spec, streams, attributes)
	if err != nil {
		streams.closeAll()
		_ = winCloseHandle(job)
		return nil, err
	}
	observe(r.Observe, "start", spec.Path, int(info.ProcessId))
	cleanupCreated := func() {
		_ = winTerminateProcess(info.Process, 1)
		_, _ = winWaitForSingleObject(info.Process, 5000)
		_ = winCloseHandle(info.Thread)
		_ = winCloseHandle(info.Process)
		_ = winCloseHandle(job)
		streams.closeAll()
	}
	if err := winAssignProcessToJobObject(job, info.Process); err != nil {
		cleanupCreated()
		return nil, err
	}
	if _, err := winResumeThread(info.Thread); err != nil {
		cleanupCreated()
		return nil, err
	}
	_ = winCloseHandle(info.Thread)
	streams.closeChildren()
	child := &Child{process: info.Process, job: job, containment: spec.Containment, pid: int(info.ProcessId), ctx: ctx, path: spec.Path,
		observe: r.Observe, streams: streams, observedExit: make(chan struct{}), result: make(chan processResult, 1),
		watchDone: make(chan struct{}), pumpDone: make(chan struct{}), targetValid: true,
		pumpErr: make(chan error, len(streams.pumps))}
	child.startPumps()
	go child.reap()
	go child.watchCancellation()
	return child, nil
}

func createSuspendedProcess(path string, spec Spec, streams *preparedStreams,
	attributes *windows.ProcThreadAttributeListContainer) (windows.ProcessInformation, error) {
	app, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return windows.ProcessInformation{}, err
	}
	commandLine, err := windows.UTF16FromString(joinCommandLine(path, spec.Args))
	if err != nil {
		return windows.ProcessInformation{}, err
	}
	environment := environmentBlock(spec.Env)
	var directory *uint16
	if spec.Dir != "" {
		directory, err = windows.UTF16PtrFromString(spec.Dir)
		if err != nil {
			return windows.ProcessInformation{}, err
		}
	}
	startup := windows.StartupInfoEx{}
	startup.Cb = uint32(unsafe.Sizeof(startup))
	startup.Flags = windows.STARTF_USESTDHANDLES
	startup.StdInput, startup.StdOutput, startup.StdErr = streams.stdin, streams.stdout, streams.stderr
	startup.ProcThreadAttributeList = attributes.List()
	var info windows.ProcessInformation
	flags := uint32(windows.EXTENDED_STARTUPINFO_PRESENT | windows.CREATE_SUSPENDED | windows.CREATE_UNICODE_ENVIRONMENT)
	err = winCreateProcess(app, &commandLine[0], nil, nil, true, flags, &environment[0], directory,
		(*windows.StartupInfo)(unsafe.Pointer(&startup)), &info)
	return info, err
}

func (c *Child) PID() int       { return c.pid }
func (c *Child) pumpCount() int { return len(c.streams.pumps) }

func (c *Child) RetainTree() (*TreeHandle, error) {
	c.lifeMu.Lock()
	defer c.lifeMu.Unlock()
	if c.containment != ContainmentInheritTree || !c.targetValid {
		return nil, ErrTreeUnavailable
	}
	var retained windows.Handle
	if err := winDuplicateHandle(windows.CurrentProcess(), c.job, windows.CurrentProcess(), &retained,
		0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
		return nil, err
	}
	return newTreeHandle(func() error { return terminateJob(retained) }, func() error { return winCloseHandle(retained) }), nil
}

func (c *Child) KillTree() error {
	c.lifeMu.Lock()
	defer c.lifeMu.Unlock()
	return c.killTreeLocked()
}

func (c *Child) killTreeLocked() error {
	if !c.targetValid || c.killIssued {
		return c.killErr
	}
	c.killIssued = true
	c.killErr = terminateJob(c.job)
	return c.killErr
}

func (c *Child) Wait() error {
	didWait := false
	c.waitOnce.Do(func() {
		didWait = true
		result := <-c.result
		<-c.watchDone
		c.lifeMu.Lock()
		cancelErr := c.cancelErr
		c.lifeMu.Unlock()
		timedOut, pumpErr := c.joinPumps()
		c.lifeMu.Lock()
		c.targetValid = false
		_ = winCloseHandle(c.process)
		_ = winCloseHandle(c.job)
		c.lifeMu.Unlock()
		if cancelErr != nil {
			c.waitErr = cancelErr
		} else {
			switch {
			case result.err != nil:
				c.waitErr = result.err
			case result.code != 0:
				c.waitErr = &ExitError{Code: result.code}
			case pumpErr != nil:
				c.waitErr = pumpErr
			case timedOut:
				c.waitErr = ErrWaitDelay
			}
		}
		observe(c.observe, "exit", c.path, c.pid)
	})
	if !didWait {
		return ErrAlreadyWaited
	}
	return c.waitErr
}

func (c *Child) joinPumps() (bool, error) {
	if c.streams.waitDelay == 0 {
		<-c.pumpDone
		select {
		case err := <-c.pumpErr:
			return false, err
		default:
			return false, nil
		}
	}
	timer := time.NewTimer(c.streams.waitDelay)
	defer timer.Stop()
	select {
	case <-c.pumpDone:
		select {
		case err := <-c.pumpErr:
			return false, err
		default:
			return false, nil
		}
	case <-timer.C:
		var pumpErr error
		select {
		case pumpErr = <-c.pumpErr:
		default:
		}
		c.streams.emergencyClose()
		_ = c.KillTree()
		<-c.pumpDone
		return true, pumpErr
	}
}

func (c *Child) startPumps() {
	var group sync.WaitGroup
	group.Add(len(c.streams.pumps))
	for _, pump := range c.streams.pumps {
		go func(run func() error) {
			defer group.Done()
			if err := run(); err != nil {
				select {
				case c.pumpErr <- err:
				default:
				}
			}
		}(pump)
	}
	go func() { group.Wait(); close(c.pumpDone) }()
}

func (c *Child) reap() {
	_, err := winWaitForSingleObject(c.process, windows.INFINITE)
	var code uint32
	if err == nil {
		err = winGetExitCodeProcess(c.process, &code)
	}
	c.lifeMu.Lock()
	c.processExited = true
	close(c.observedExit)
	c.lifeMu.Unlock()
	c.result <- processResult{code: code, err: err}
}

func (c *Child) watchCancellation() {
	defer close(c.watchDone)
	select {
	case <-c.observedExit:
	case <-c.ctx.Done():
		c.lifeMu.Lock()
		if c.processExited {
			c.lifeMu.Unlock()
			return
		}
		c.cancelErr = context.Cause(c.ctx)
		_ = c.killTreeLocked()
		c.lifeMu.Unlock()
		c.streams.emergencyClose()
	}
}

func joinCommandLine(path string, args []string) string {
	parts := make([]string, 1, len(args)+1)
	parts[0] = windows.EscapeArg(path)
	for _, arg := range args {
		parts = append(parts, windows.EscapeArg(arg))
	}
	return strings.Join(parts, " ")
}

func environmentBlock(environment []string) []uint16 {
	environment = append([]string(nil), environment...)
	sort.SliceStable(environment, func(i, j int) bool { return strings.ToUpper(environment[i]) < strings.ToUpper(environment[j]) })
	block := make([]uint16, 0)
	for _, entry := range environment {
		block = append(block, utf16.Encode([]rune(entry))...)
		block = append(block, 0)
	}
	if len(block) == 0 {
		return []uint16{0, 0}
	}
	return append(block, 0)
}
