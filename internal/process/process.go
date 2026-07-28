package process

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

type Containment uint8

const (
	ContainmentOwnTree Containment = iota + 1
	ContainmentForegroundTree
	ContainmentInheritTree
)

type Spec struct {
	Path          string
	Args          []string
	Env           []string
	Dir           string
	Stdin         io.Reader
	Stdout        io.Writer
	Stderr        io.Writer
	Containment   Containment
	ForegroundTTY *os.File
	WaitDelay     time.Duration
}

type ProcessEvent struct {
	Phase string
	PID   int
	Path  string
}

type Runner struct{ Observe func(ProcessEvent) }

type ExitError struct{ Code uint32 }

func (e *ExitError) Error() string { return fmt.Sprintf("process exited with code %d", e.Code) }
func (e *ExitError) ExitCode() int { return int(e.Code) }

var ErrAlreadyWaited = errors.New("process: Wait called more than once")
var ErrWaitDelay = errors.New("process: I/O pumps did not finish before WaitDelay")

func (r Runner) Run(ctx context.Context, spec Spec) error {
	child, err := r.Start(ctx, spec)
	if err != nil {
		return err
	}
	return child.Wait()
}

func validateSpec(ctx context.Context, spec Spec) error {
	if ctx == nil {
		return errors.New("process: nil context")
	}
	if spec.Path == "" {
		return errors.New("process: empty executable path")
	}
	if spec.WaitDelay < 0 {
		return errors.New("process: negative WaitDelay")
	}
	if spec.Containment < ContainmentOwnTree || spec.Containment > ContainmentInheritTree {
		return errors.New("process: invalid containment")
	}
	return nil
}

func observe(fn func(ProcessEvent), phase, path string, pid int) {
	if fn != nil {
		fn(ProcessEvent{Phase: phase, PID: pid, Path: path})
	}
}
