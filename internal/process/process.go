package process

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"sync"
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

type emergencyClosers struct {
	once  sync.Once
	items []io.Closer
}

func (c *emergencyClosers) add(stream any) {
	closer, ok := stream.(io.Closer)
	if !ok {
		return
	}
	if _, direct := stream.(*os.File); direct {
		return
	}
	for _, existing := range c.items {
		if sameObject(existing, closer) {
			return
		}
	}
	c.items = append(c.items, closer)
}

func (c *emergencyClosers) close() {
	c.once.Do(func() {
		for _, closer := range c.items {
			_ = closer.Close()
		}
	})
}

func sameObject(a, b any) bool {
	av, bv := reflect.ValueOf(a), reflect.ValueOf(b)
	if !av.IsValid() || !bv.IsValid() || av.Type() != bv.Type() {
		return false
	}
	if av.Type().Comparable() {
		return av.Interface() == bv.Interface()
	}
	return av.Kind() == reflect.Pointer && av.Pointer() == bv.Pointer()
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

func sharedWriter(a, b io.Writer) bool { return a != nil && b != nil && sameObject(a, b) }

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
