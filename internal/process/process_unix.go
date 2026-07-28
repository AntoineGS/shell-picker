//go:build !windows

package process

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"reflect"
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

	waitOnce  sync.Once
	waitErr   error
	killOnce  sync.Once
	killErr   error
	exited    chan struct{}
	watchDone chan struct{}
	cancelWon chan error
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
	child := &Child{cmd: cmd, ctx: ctx, containment: spec.Containment, path: spec.Path, observe: r.Observe,
		ttyFD: ttyFD, previousPGR: previousPGR, exited: make(chan struct{}), watchDone: make(chan struct{}),
		cancelWon: make(chan error, 1), streams: streams, pumpDone: make(chan struct{}),
		pumpErr: make(chan error, len(streams.pumps))}
	child.waitDelay = spec.WaitDelay
	child.childTTYFD = childTTYFD
	observe(r.Observe, "start", spec.Path, cmd.Process.Pid)
	child.startPumps()
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
	c.killOnce.Do(func() {
		group := c.PID()
		if c.containment == ContainmentInheritTree {
			group, c.killErr = syscall.Getpgid(c.PID())
			if c.killErr != nil {
				return
			}
		}
		c.killErr = syscall.Kill(-group, syscall.SIGKILL)
		if errors.Is(c.killErr, syscall.ESRCH) {
			c.killErr = nil
		}
	})
	return c.killErr
}

func (c *Child) Wait() error {
	didWait := false
	c.waitOnce.Do(func() {
		didWait = true
		waitErr := c.cmd.Wait()
		close(c.exited)
		<-c.watchDone
		timedOut, pumpErr := c.joinPumps()
		if c.containment == ContainmentForegroundTree {
			_ = restoreForegroundPGR(c.ttyFD, c.previousPGR)
		}
		select {
		case c.waitErr = <-c.cancelWon:
		default:
			if c.waitErr = unixWaitError(waitErr); c.waitErr == nil {
				if pumpErr != nil {
					c.waitErr = pumpErr
				} else if timedOut {
					c.waitErr = ErrWaitDelay
				}
			}
		}
		observe(c.observe, "exit", c.path, c.PID())
	})
	if !didWait {
		return ErrAlreadyWaited
	}
	return c.waitErr
}

func (c *Child) watchCancellation() {
	defer close(c.watchDone)
	select {
	case <-c.exited:
	case <-c.ctx.Done():
		select {
		case <-c.exited:
			return
		default:
		}
		c.cancelWon <- context.Cause(c.ctx)
		_ = c.KillTree()
	}
}

type unixOwnedFile struct {
	file *os.File
	once sync.Once
}

func (f *unixOwnedFile) close() { f.once.Do(func() { _ = f.file.Close() }) }

type unixStreams struct {
	stdin          io.Reader
	stdout, stderr io.Writer
	children       []*os.File
	parents        []*unixOwnedFile
	pumps          []func() error
}

func prepareUnixStreams(spec Spec) (*unixStreams, error) {
	streams := &unixStreams{stdin: spec.Stdin, stdout: spec.Stdout, stderr: spec.Stderr}
	if spec.Stdin != nil {
		if _, ok := spec.Stdin.(*os.File); !ok {
			read, write, err := os.Pipe()
			if err != nil {
				return nil, err
			}
			parent := &unixOwnedFile{file: write}
			streams.stdin = read
			streams.children, streams.parents = append(streams.children, read), append(streams.parents, parent)
			streams.pumps = append(streams.pumps, func() error { _, err := io.Copy(write, spec.Stdin); parent.close(); return err })
		}
	}
	var err error
	if streams.stdout, err = streams.prepareOutput(spec.Stdout); err != nil {
		streams.closeAll()
		return nil, err
	}
	if streams.stderr, err = streams.prepareOutput(spec.Stderr); err != nil {
		streams.closeAll()
		return nil, err
	}
	return streams, nil
}

func (s *unixStreams) prepareOutput(writer io.Writer) (io.Writer, error) {
	if writer == nil {
		return nil, nil
	}
	if _, ok := writer.(*os.File); ok {
		return writer, nil
	}
	read, write, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	parent := &unixOwnedFile{file: read}
	s.children, s.parents = append(s.children, write), append(s.parents, parent)
	s.pumps = append(s.pumps, func() error { _, err := io.Copy(writer, read); parent.close(); return err })
	return write, nil
}

func (s *unixStreams) closeChildren() {
	for _, file := range s.children {
		_ = file.Close()
	}
	s.children = nil
}
func (s *unixStreams) closeParents() {
	for _, file := range s.parents {
		file.close()
	}
}
func (s *unixStreams) closeAll() { s.closeChildren(); s.closeParents() }

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
		c.streams.closeParents()
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

func restoreForegroundPGR(fd, group int) error {
	signal.Ignore(syscall.SIGTTOU)
	defer signal.Reset(syscall.SIGTTOU)
	for attempts := 0; attempts < 16; attempts++ {
		_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), syscall.TIOCSPGRP, uintptr(unsafe.Pointer(&group)))
		if errno == syscall.EINTR {
			continue
		}
		if errno != 0 {
			return errno
		}
		return nil
	}
	return syscall.EINTR
}
