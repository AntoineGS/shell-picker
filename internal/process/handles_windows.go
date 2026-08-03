//go:build windows

package process

import (
	"fmt"
	"io"
	"os"
	"reflect"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	winCreateFile           = windows.CreateFile
	winDuplicateHandle      = windows.DuplicateHandle
	winCreatePipe           = windows.CreatePipe
	winSetHandleInformation = windows.SetHandleInformation
	winCloseHandle          = windows.CloseHandle
	winCloseFile            = func(file *os.File) error { return file.Close() }
)

type ownedFile struct {
	file *os.File
	once sync.Once
	err  error
}

func (f *ownedFile) close() error {
	f.once.Do(func() { f.err = winCloseFile(f.file) })
	return f.err
}

type preparedStreams struct {
	stdin, stdout, stderr windows.Handle
	children              []windows.Handle
	parents               []*ownedFile
	pumps                 []func() error
	waitDelay             time.Duration
	closers               emergencyClosers
	stdinCloser           *onceCloser
}

func prepareStreams(spec Spec) (*preparedStreams, error) {
	p := &preparedStreams{waitDelay: spec.WaitDelay}
	stdout, stderr := spec.Stdout, spec.Stderr
	if sameWriter(stdout, stderr) {
		p.closers.add(stdout)
		locked := &lockedWriter{writer: stdout}
		stdout, stderr = locked, locked
	}
	var err error
	if p.stdin, err = p.prepareInput(spec); err != nil {
		p.closeAll()
		return nil, fmt.Errorf("prepare stdin: %w", err)
	}
	if p.stdout, err = p.prepareOutput(stdout); err != nil {
		p.closeAll()
		return nil, fmt.Errorf("prepare stdout: %w", err)
	}
	if p.stderr, err = p.prepareOutput(stderr); err != nil {
		p.closeAll()
		return nil, fmt.Errorf("prepare stderr: %w", err)
	}
	return p, nil
}

func (p *preparedStreams) prepareInput(spec Spec) (windows.Handle, error) {
	reader := spec.Stdin
	if reader == nil {
		return p.duplicateNull(windows.GENERIC_READ)
	}
	if file, ok := reader.(*os.File); ok {
		return p.duplicate(windows.Handle(file.Fd()))
	}
	if closer := optInStdinCloser(spec); closer != nil {
		p.stdinCloser = closer
		p.closers.add(closer)
	} else {
		p.closers.add(reader)
	}
	child, parent, err := inheritablePipe(true)
	if err != nil {
		return 0, err
	}
	p.children = append(p.children, child)
	p.parents = append(p.parents, parent)
	p.pumps = append(p.pumps, func() error {
		_, copyErr := io.Copy(parent.file, reader)
		closeErr := parent.close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	return child, nil
}

func (p *preparedStreams) prepareOutput(writer io.Writer) (windows.Handle, error) {
	if writer == nil {
		return p.duplicateNull(windows.GENERIC_WRITE)
	}
	if file, ok := writer.(*os.File); ok {
		return p.duplicate(windows.Handle(file.Fd()))
	}
	p.closers.add(writer)
	child, parent, err := inheritablePipe(false)
	if err != nil {
		return 0, err
	}
	p.children = append(p.children, child)
	p.parents = append(p.parents, parent)
	p.pumps = append(p.pumps, func() error {
		_, copyErr := io.Copy(writer, parent.file)
		closeErr := parent.close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	return child, nil
}

func (p *preparedStreams) duplicateNull(access uint32) (windows.Handle, error) {
	name, _ := windows.UTF16PtrFromString("NUL")
	handle, err := winCreateFile(name, access, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return 0, err
	}
	duplicate, duplicateErr := duplicateInheritable(handle)
	closeErr := winCloseHandle(handle)
	if duplicateErr != nil {
		return 0, duplicateErr
	}
	if closeErr != nil {
		_ = winCloseHandle(duplicate)
		return 0, closeErr
	}
	p.children = append(p.children, duplicate)
	return duplicate, nil
}

func (p *preparedStreams) duplicate(handle windows.Handle) (windows.Handle, error) {
	duplicate, err := duplicateInheritable(handle)
	if err == nil {
		p.children = append(p.children, duplicate)
	}
	return duplicate, err
}

func duplicateInheritable(handle windows.Handle) (windows.Handle, error) {
	var duplicate windows.Handle
	err := winDuplicateHandle(windows.CurrentProcess(), handle, windows.CurrentProcess(), &duplicate,
		0, true, windows.DUPLICATE_SAME_ACCESS)
	return duplicate, err
}

func inheritablePipe(input bool) (windows.Handle, *ownedFile, error) {
	attributes := &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), InheritHandle: 1}
	var read, write windows.Handle
	if err := winCreatePipe(&read, &write, attributes, 0); err != nil {
		return 0, nil, err
	}
	child, parent, name := read, write, "process-stdin"
	if !input {
		child, parent, name = write, read, "process-output"
	}
	if err := winSetHandleInformation(parent, windows.HANDLE_FLAG_INHERIT, 0); err != nil {
		_ = winCloseHandle(read)
		_ = winCloseHandle(write)
		return 0, nil, err
	}
	return child, &ownedFile{file: os.NewFile(uintptr(parent), name)}, nil
}

func (p *preparedStreams) closeChildren() {
	for _, handle := range p.children {
		_ = winCloseHandle(handle)
	}
	p.children = nil
}

func (p *preparedStreams) closeParents() {
	for _, file := range p.parents {
		_ = file.close()
	}
}

func (p *preparedStreams) closeAll()       { p.closeChildren(); p.closeParents() }
func (p *preparedStreams) emergencyClose() { p.closeParents(); p.closers.close() }
func (p *preparedStreams) closeStdin() {
	if p.stdinCloser != nil {
		_ = p.stdinCloser.Close()
	}
}

type lockedWriter struct {
	mu     sync.Mutex
	writer io.Writer
}

func (w *lockedWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writer.Write(data)
}

func sameWriter(a, b io.Writer) bool {
	if a == nil || b == nil {
		return false
	}
	av, bv := reflect.ValueOf(a), reflect.ValueOf(b)
	return av.Kind() == reflect.Pointer && bv.Kind() == reflect.Pointer && av.Type() == bv.Type() && av.Pointer() == bv.Pointer()
}
