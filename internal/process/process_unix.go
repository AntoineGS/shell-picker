//go:build !windows

package process

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

type Child struct {
	cmd         *exec.Cmd
	ctx         context.Context
	containment Containment
	path        string
	observe     func(ProcessEvent)
	ttyFD       int
	previousPGR int
	waitDelay   time.Duration
	childTTYFD  int
	streams     *unixStreams
	pumpDone    chan struct{}
	pumpErr     chan error

	waitOnce      sync.Once
	waitErr       error
	watchDone     chan struct{}
	observedExit  chan struct{}
	result        chan error
	lifeMu        sync.Mutex
	processExited bool
	targetValid   bool
	killIssued    bool
	killErr       error
	cancelErr     error
	pgid          int
}

func (r Runner) Start(ctx context.Context, spec Spec) (*Child, error) {
	if err := validateSpec(ctx, spec); err != nil {
		return nil, err
	}
	if spec.Containment == ContainmentForegroundTree && spec.ForegroundTTY == nil {
		return nil, errors.New("process: foreground containment requires a terminal")
	}
	observe(r.Observe, "attempt", spec.Path, 0)
	path, err := exec.LookPath(spec.Path)
	if err != nil {
		if os.IsNotExist(err) || errors.Is(err, syscall.ENOENT) {
			return nil, fmt.Errorf("%w: %v", exec.ErrNotFound, err)
		}
		return nil, err
	}
	cmd := exec.Command(path, spec.Args...)
	cmd.Env, cmd.Dir = spec.Env, spec.Dir
	streams, err := prepareUnixStreams(spec)
	if err != nil {
		return nil, err
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = streams.stdin, streams.stdout, streams.stderr
	attr := &syscall.SysProcAttr{}
	ttyFD, childTTYFD, previousPGR := 0, 0, 0
	switch spec.Containment {
	case ContainmentOwnTree:
		attr.Setpgid = true
		setParentDeathSignal(attr)
	case ContainmentForegroundTree:
		ttyFD = int(spec.ForegroundTTY.Fd())
		childTTYFD = 3 + len(cmd.ExtraFiles)
		previousPGR, err = foregroundPGR(ttyFD)
		if err != nil {
			streams.closeAll()
			return nil, fmt.Errorf("read foreground process group: %w", err)
		}
		cmd.ExtraFiles = append(cmd.ExtraFiles, spec.ForegroundTTY)
		attr.Setpgid, attr.Foreground, attr.Ctty = true, true, ttyFD
		setParentDeathSignal(attr)
	}
	cmd.SysProcAttr = attr
	if err := cmd.Start(); err != nil {
		streams.closeAll()
		return nil, err
	}
	streams.closeChildren()
	pgid := cmd.Process.Pid
	if spec.Containment == ContainmentInheritTree {
		pgid, err = syscall.Getpgid(cmd.Process.Pid)
		if err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			streams.closeAll()
			return nil, err
		}
	}
	child := &Child{cmd: cmd, ctx: ctx, containment: spec.Containment, path: spec.Path, observe: r.Observe,
		ttyFD: ttyFD, previousPGR: previousPGR, watchDone: make(chan struct{}), observedExit: make(chan struct{}),
		streams: streams, pumpDone: make(chan struct{}), pumpErr: make(chan error, len(streams.pumps)),
		targetValid: true, pgid: pgid, result: make(chan error, 1)}
	child.waitDelay = spec.WaitDelay
	child.childTTYFD = childTTYFD
	observe(r.Observe, "start", spec.Path, cmd.Process.Pid)
	child.startPumps()
	go child.reap()
	go child.watchCancellation()
	return child, nil
}

func setParentDeathSignal(attr *syscall.SysProcAttr) {
	field := reflect.ValueOf(attr).Elem().FieldByName("Pdeathsig")
	if field.IsValid() && field.CanSet() {
		field.SetInt(int64(syscall.SIGKILL))
	}
}

func (c *Child) PID() int { return c.cmd.Process.Pid }

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
	c.killErr = syscall.Kill(-c.pgid, syscall.SIGKILL)
	if errors.Is(c.killErr, syscall.ESRCH) {
		c.killErr = nil
	}
	return c.killErr
}

func (c *Child) Wait() error {
	didWait := false
	c.waitOnce.Do(func() {
		didWait = true
		waitErr := <-c.result
		<-c.watchDone
		c.lifeMu.Lock()
		cancelErr := c.cancelErr
		c.lifeMu.Unlock()
		timedOut, pumpErr := c.joinPumps()
		if c.containment == ContainmentForegroundTree {
			_ = restoreForegroundPGR(c.ttyFD, c.previousPGR)
		}
		if cancelErr != nil {
			c.waitErr = cancelErr
		} else {
			if c.waitErr = unixWaitError(waitErr); c.waitErr == nil {
				if pumpErr != nil {
					c.waitErr = pumpErr
				} else if timedOut {
					c.waitErr = ErrWaitDelay
				}
			}
		}
		c.lifeMu.Lock()
		c.targetValid = false
		c.lifeMu.Unlock()
		observe(c.observe, "exit", c.path, c.PID())
	})
	if !didWait {
		return ErrAlreadyWaited
	}
	return c.waitErr
}

func (c *Child) reap() {
	err := c.cmd.Wait()
	c.lifeMu.Lock()
	c.processExited = true
	close(c.observedExit)
	c.lifeMu.Unlock()
	c.result <- err
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

func (c *Child) joinPumps() (bool, error) {
	if c.waitDelay == 0 {
		<-c.pumpDone
		select {
		case err := <-c.pumpErr:
			return false, err
		default:
			return false, nil
		}
	}
	timer := time.NewTimer(c.waitDelay)
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

func unixWaitError(err error) error {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return err
	}
	code := exitErr.ExitCode()
	if status, ok := exitErr.Sys().(syscall.WaitStatus); code < 0 && ok && status.Signaled() {
		code = 128 + int(status.Signal())
	}
	return &ExitError{Code: uint32(code)}
}

func foregroundPGR(fd int) (int, error) {
	var group int
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), syscall.TIOCGPGRP, uintptr(unsafe.Pointer(&group)))
	if errno != 0 {
		return 0, errno
	}
	return group, nil
}

var setForegroundPGR = func(fd int, group *int) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), syscall.TIOCSPGRP, uintptr(unsafe.Pointer(group)))
	if errno != 0 {
		return errno
	}
	return nil
}

func restoreForegroundPGR(fd, group int) (result error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	blocked := sigttouMask()
	var previous threadSigset
	if err := pthreadSigmask(threadSigBlock, &blocked, &previous); err != nil {
		return err
	}
	defer func() {
		if err := pthreadSigmask(threadSigSetmask, &previous, nil); result == nil {
			result = err
		}
	}()
	for attempts := 0; attempts < 16; attempts++ {
		err := setForegroundPGR(fd, &group)
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		return err
	}
	return syscall.EINTR
}
