//go:build !windows

package process

import (
	"io"
	"os"
	"sync"
)

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
	stdinCloser    *onceCloser
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
			streams.stdinCloser = optInStdinCloser(spec)
			if streams.stdinCloser != nil {
				streams.closers.add(streams.stdinCloser)
			} else {
				streams.closers.add(spec.Stdin)
			}
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
func (s *unixStreams) closeStdin() {
	if s.stdinCloser != nil {
		_ = s.stdinCloser.Close()
	}
}
