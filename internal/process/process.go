package process

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"sync"
	"syscall"
	"time"
)

type Containment uint8

const (
	ContainmentOwnTree Containment = iota + 1
	ContainmentForegroundTree
	ContainmentInheritTree
)

type Spec struct {
	Path string
	Args []string
	Env  []string
	Dir  string
	// Non-file streams must be cooperative or promptly closable. Pumped closers
	// require stable pointer identity and are never structurally compared.
	// Emergency cleanup closes them once; direct files remain caller-owned.
	Stdin            io.Reader
	Stdout           io.Writer
	Stderr           io.Writer
	CloseStdinOnExit bool
	Containment      Containment
	ForegroundTTY    *os.File
	ExtraFiles       []*os.File
	WaitDelay        time.Duration
}

type ProcessEvent struct {
	Phase string
	PID   int
	Path  string
}

type Runner struct {
	Observe func(ProcessEvent)
	// BeforeStart is a fail-only inspection hook. Run calls it after generic
	// validation and always continues to the real Start when it returns nil.
	BeforeStart func(Spec) error
}
type ExitError struct{ Code uint32 }

func (e *ExitError) Error() string { return fmt.Sprintf("process exited with code %d", e.Code) }
func (e *ExitError) ExitCode() int { return int(e.Code) }

var ErrAlreadyWaited = errors.New("process: Wait called more than once")
var ErrWaitDelay = errors.New("process: I/O pumps did not finish before WaitDelay")
var ErrInvalidStream = errors.New("process: pumped io.Closer requires stable pointer identity")
var ErrExitObserver = errors.New("process: exit observer failed")
var ErrUnsupportedPlatform, ErrTreeUnavailable = errors.New("process: unsupported Unix process backend"), errors.New("process: inherited tree unavailable")

type waitDelayError struct{ error }

func (err waitDelayError) Unwrap() []error { return []error{err.error, ErrWaitDelay} }
func preserveWaitDelay(primary error, timedOut bool) error {
	if !timedOut {
		return primary
	}
	if primary == nil {
		return ErrWaitDelay
	}
	return waitDelayError{primary}
}

type TreeHandle struct {
	mu                  sync.Mutex
	kill, close         func() error
	killed, closed      bool
	killErr, closeError error
}

func newTreeHandle(kill, close func() error) *TreeHandle {
	return &TreeHandle{kill: kill, close: close}
}
func (handle *TreeHandle) KillTree() error {
	handle.mu.Lock()
	defer handle.mu.Unlock()
	if handle.closed {
		return nil
	}
	if !handle.killed {
		handle.killed = true
		handle.killErr = handle.kill()
	}
	return handle.killErr
}
func (handle *TreeHandle) Close() error {
	handle.mu.Lock()
	defer handle.mu.Unlock()
	if !handle.closed {
		handle.closed = true
		if handle.close != nil {
			handle.closeError = handle.close()
		}
	}
	return handle.closeError
}
func validateSpec(ctx context.Context, spec Spec) error {
	if ctx == nil {
		return errors.New("process: nil context")
	}
	if spec.Path == "" {
		return errors.New("process: empty executable path")
	}
	if err := validateEnvironment(spec.Env); err != nil {
		return err
	}
	if spec.WaitDelay < 0 {
		return errors.New("process: negative WaitDelay")
	}
	if spec.Containment < ContainmentOwnTree || spec.Containment > ContainmentInheritTree {
		return errors.New("process: invalid containment")
	}
	for _, stream := range []any{spec.Stdin, spec.Stdout, spec.Stderr} {
		if err := validateStream(stream); err != nil {
			return err
		}
	}
	return nil
}

func validateStream(stream any) error {
	if stream == nil {
		return nil
	}
	value := reflect.ValueOf(stream)
	if value.Kind() == reflect.Pointer && value.IsNil() {
		return ErrInvalidStream
	}
	if _, direct := stream.(*os.File); direct {
		return nil
	}
	if _, closable := stream.(io.Closer); !closable {
		return nil
	}
	if value.Kind() != reflect.Pointer {
		return ErrInvalidStream
	}
	return nil
}
func observe(fn func(ProcessEvent), phase, path string, pid int) {
	if fn != nil {
		fn(ProcessEvent{Phase: phase, PID: pid, Path: path})
	}
}

type emergencyClosers struct {
	once  sync.Once
	items []io.Closer
	seen  map[closerIdentity]struct{}
}

type onceCloser struct {
	once   sync.Once
	closer io.Closer
	err    error
}

func (closer *onceCloser) Close() error {
	closer.once.Do(func() { closer.err = closer.closer.Close() })
	return closer.err
}

type closerIdentity struct {
	typ     reflect.Type
	pointer uintptr
}

func (c *emergencyClosers) add(stream any) {
	closer, ok := stream.(io.Closer)
	if !ok {
		return
	}
	if _, direct := stream.(*os.File); direct {
		return
	}
	identity, ok := pointerIdentity(stream)
	if !ok {
		return
	}
	if c.seen == nil {
		c.seen = make(map[closerIdentity]struct{})
	}
	if _, exists := c.seen[identity]; exists {
		return
	}
	c.seen[identity] = struct{}{}
	c.items = append(c.items, closer)
}
func (c *emergencyClosers) close() {
	c.once.Do(func() {
		for _, closer := range c.items {
			_ = closer.Close()
		}
	})
}
func pointerIdentity(value any) (closerIdentity, bool) {
	ref := reflect.ValueOf(value)
	if !ref.IsValid() || ref.Kind() != reflect.Pointer || ref.IsNil() {
		return closerIdentity{}, false
	}
	return closerIdentity{typ: ref.Type(), pointer: ref.Pointer()}, true
}

func samePointer(left, right any) bool {
	leftID, leftOK := pointerIdentity(left)
	rightID, rightOK := pointerIdentity(right)
	return leftOK && rightOK && leftID == rightID
}

func optInStdinCloser(spec Spec) *onceCloser {
	if !spec.CloseStdinOnExit {
		return nil
	}
	closer, ok := spec.Stdin.(io.Closer)
	if !ok {
		return nil
	}
	if _, direct := spec.Stdin.(*os.File); direct || samePointer(spec.Stdin, spec.Stdout) || samePointer(spec.Stdin, spec.Stderr) {
		return nil
	}
	return &onceCloser{closer: closer}
}

type serializedWriter struct {
	mu     sync.Mutex
	writer io.Writer
}

func (w *serializedWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writer.Write(data)
}
func sharedWriter(a, b io.Writer) bool {
	left, ok := pointerIdentity(a)
	if !ok {
		return false
	}
	right, ok := pointerIdentity(b)
	return ok && left == right
}

type exitObserverEvent struct {
	PID     int
	Process bool
	Error   bool
	Exit    bool
	Data    int64
}
type exitObserverResult struct {
	N      int
	Events []exitObserverEvent
	Err    error
}

func validateKqueueObserverResults(pid int, registration, wait exitObserverResult) error {
	if err := validateObserverResult(pid, registration, true); err != nil {
		return err
	}
	return validateObserverResult(pid, wait, false)
}
func observeKqueueExit(pid int, registrationCall, waitCall func() exitObserverResult) error {
	if err := validateObserverResult(pid, registrationCall(), true); err != nil {
		return err
	}
	return validateObserverResult(pid, waitCall(), false)
}
func validateObserverResult(pid int, result exitObserverResult, registration bool) error {
	if result.Err != nil {
		return fmt.Errorf("%w: %v", ErrExitObserver, result.Err)
	}
	if result.N <= 0 || result.N > len(result.Events) {
		return fmt.Errorf("%w: invalid event count %d", ErrExitObserver, result.N)
	}
	if registration && result.N != 1 {
		return fmt.Errorf("%w: invalid registration count %d", ErrExitObserver, result.N)
	}
	for _, event := range result.Events[:result.N] {
		if event.Error {
			if event.Data != 0 {
				return fmt.Errorf("%w: %v", ErrExitObserver, syscall.Errno(event.Data))
			}
			if registration && event.PID == pid && event.Process {
				return nil
			}
			continue
		}
		if !registration && event.PID == pid && event.Process && event.Exit {
			return nil
		}
	}
	return fmt.Errorf("%w: missing expected process event", ErrExitObserver)
}
