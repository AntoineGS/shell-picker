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
	// Non-file streams must be finite/cooperative, or implement io.Closer
	// whose Close promptly unblocks pending I/O. Cancellation and WaitDelay
	// may close such streams; ordinary completion does not.
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
var ErrInvalidStream = errors.New("process: pumped io.Closer requires stable pointer identity")
var ErrExitObserver = errors.New("process: exit observer failed")
var ErrUnsupportedPlatform = errors.New("process: unsupported Unix process backend")

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
	closers        emergencyClosers
}

func prepareUnixStreams(spec Spec) (*unixStreams, error) {
	streams := &unixStreams{stdin: spec.Stdin, stdout: spec.Stdout, stderr: spec.Stderr}
	if sharedWriter(spec.Stdout, spec.Stderr) {
		streams.closers.add(spec.Stdout)
		writer := &serializedWriter{writer: spec.Stdout}
		spec.Stdout, spec.Stderr = writer, writer
	}
	streams.stdout, streams.stderr = spec.Stdout, spec.Stderr
	if spec.Stdin != nil {
		if _, ok := spec.Stdin.(*os.File); !ok {
			streams.closers.add(spec.Stdin)
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
	s.closers.add(writer)
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
func (s *unixStreams) closeAll()       { s.closeChildren(); s.closeParents() }
func (s *unixStreams) emergencyClose() { s.closeParents(); s.closers.close() }
