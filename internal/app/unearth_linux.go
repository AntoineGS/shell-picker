//go:build linux

package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AntoineGS/shell-picker/internal/fsearchdb"
	"github.com/AntoineGS/shell-picker/internal/process"
	"github.com/AntoineGS/shell-picker/internal/sessionipc"
)

const (
	unearthMaxRecordBytes = 1 << 20
	unearthMaxEntries     = 20_000_000
	unearthWaitDelay      = 2 * time.Second
)

func loadDefaultIndex(ctx context.Context, root string, environment []string) (*fsearchdb.Database, error) {
	return loadUnearth(ctx, root, environment)
}

func indexedSourceName() string { return "Unearth" }

func loadUnearth(ctx context.Context, root string, environment []string) (*fsearchdb.Database, error) {
	executable, err := findUnearth(environment)
	if err != nil {
		return nil, err
	}
	stdout, stdoutWriter := io.Pipe()
	var stderr limitedBuffer
	child, err := (process.Runner{}).Start(ctx, process.Spec{
		Path: executable, Args: []string{"--require-live-index", "--index-binary", "*", "--hidden", root},
		Env: environment, Stdout: stdoutWriter, Stderr: &stderr,
		Containment: process.ContainmentOwnTree, WaitDelay: unearthWaitDelay,
	})
	if err != nil {
		_ = stdout.Close()
		_ = stdoutWriter.Close()
		return nil, fmt.Errorf("start unearth: %w", err)
	}

	finished := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = child.KillTree()
			_ = stdoutWriter.CloseWithError(context.Cause(ctx))
		case <-finished:
		}
	}()
	waitResult := make(chan error, 1)
	go func() {
		waitErr := child.Wait()
		_ = stdoutWriter.Close()
		waitResult <- waitErr
	}()
	database, decodeErr := decodeUnearth(stdout)
	if decodeErr != nil {
		_ = child.KillTree()
		_ = stdoutWriter.CloseWithError(decodeErr)
	}
	waitErr := <-waitResult
	close(finished)
	_ = stdout.Close()
	_ = stdoutWriter.Close()
	if cause := context.Cause(ctx); cause != nil {
		return nil, cause
	}
	if decodeErr != nil {
		return nil, fmt.Errorf("decode unearth output: %w", decodeErr)
	}
	if waitErr != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail != "" {
			return nil, fmt.Errorf("unearth exited unsuccessfully: %w: %s", waitErr, detail)
		}
		return nil, fmt.Errorf("unearth exited unsuccessfully: %w", waitErr)
	}
	return database, nil
}

// streamUnearth exposes each independently validated path record as it arrives.
// Its returned error remains authoritative: callers must not accept a streamed
// prefix unless the process exits successfully.
func streamUnearth(ctx context.Context, root string, environment []string, emit func(fsearchdb.Entry) error) error {
	executable, err := findUnearth(environment)
	if err != nil {
		return err
	}
	stdout, stdoutWriter := io.Pipe()
	var stderr limitedBuffer
	child, err := (process.Runner{}).Start(ctx, process.Spec{
		Path: executable, Args: []string{"--require-live-index", "--index-binary", "--stream-index-binary", "--hidden", "*", root},
		Env: environment, Stdout: stdoutWriter, Stderr: &stderr,
		Containment: process.ContainmentOwnTree, WaitDelay: unearthWaitDelay,
	})
	if err != nil {
		_ = stdout.Close()
		_ = stdoutWriter.Close()
		return fmt.Errorf("start unearth: %w", err)
	}

	finished := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = child.KillTree()
			_ = stdoutWriter.CloseWithError(context.Cause(ctx))
		case <-finished:
		}
	}()
	waitResult := make(chan error, 1)
	go func() {
		waitErr := child.Wait()
		_ = stdoutWriter.Close()
		waitResult <- waitErr
	}()
	decodeErr := decodeUnearthEach(bufio.NewReaderSize(stdout, 128<<10), emit)
	if decodeErr != nil {
		_ = child.KillTree()
		_ = stdoutWriter.CloseWithError(decodeErr)
	}
	waitErr := <-waitResult
	close(finished)
	_ = stdout.Close()
	_ = stdoutWriter.Close()
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	if decodeErr != nil {
		return fmt.Errorf("decode unearth output: %w", decodeErr)
	}
	if waitErr != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail != "" {
			return fmt.Errorf("unearth exited unsuccessfully: %w: %s", waitErr, detail)
		}
		return fmt.Errorf("unearth exited unsuccessfully: %w", waitErr)
	}
	return nil
}

func decodeUnearth(reader io.Reader) (*fsearchdb.Database, error) {
	database := &fsearchdb.Database{Entries: make([]fsearchdb.Entry, 0)}
	err := decodeUnearthEach(reader, func(entry fsearchdb.Entry) error {
		database.Entries = append(database.Entries, entry)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return database, nil
}

func decodeUnearthEach(reader io.Reader, emit func(fsearchdb.Entry) error) error {
	var total uint64
	var count int
	var sizeBytes [4]byte
	for {
		_, err := io.ReadFull(reader, sizeBytes[:])
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read record size: %w", err)
		}
		size := binary.LittleEndian.Uint32(sizeBytes[:])
		if size == 0 {
			return errors.New("zero-length path record")
		}
		if size > unearthMaxRecordBytes {
			return fmt.Errorf("path record size %d exceeds %d-byte limit", size, unearthMaxRecordBytes)
		}
		if count >= unearthMaxEntries {
			return fmt.Errorf("path count exceeds %d-entry limit", unearthMaxEntries)
		}
		path := make([]byte, int(size))
		if _, err := io.ReadFull(reader, path); err != nil {
			return fmt.Errorf("read path record: %w", err)
		}
		if path[0] != '/' || bytes.IndexByte(path, 0) >= 0 {
			return fmt.Errorf("invalid unearth path record")
		}
		total += uint64(len(path))
		if total > uint64(sessionipc.MaxLoadResponseBytes) {
			return fmt.Errorf("unearth paths exceed %d-byte limit", sessionipc.MaxLoadResponseBytes)
		}
		isDir := path[len(path)-1] == '/'
		if isDir && len(path) > 1 {
			path = path[:len(path)-1]
		}
		if err := emit(fsearchdb.Entry{Path: path, IsDir: isDir}); err != nil {
			return err
		}
		count++
	}
}

func findUnearth(environment []string) (string, error) {
	pathValue := ""
	for _, entry := range environment {
		if key, value, ok := strings.Cut(entry, "="); ok && key == "PATH" {
			pathValue = value
			break
		}
	}
	for _, directory := range filepath.SplitList(pathValue) {
		if directory == "" {
			directory = "."
		}
		candidate := filepath.Join(directory, "unearth")
		info, err := os.Stat(candidate)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
			continue
		}
		absolute, err := filepath.Abs(candidate)
		if err != nil {
			return "", fmt.Errorf("resolve unearth executable: %w", err)
		}
		return absolute, nil
	}
	return "", errors.New("unearth executable not found in child PATH")
}

type limitedBuffer struct {
	bytes.Buffer
}

func (buffer *limitedBuffer) Write(data []byte) (int, error) {
	const limit = 64 << 10
	original := len(data)
	remaining := limit - buffer.Len()
	if remaining > 0 {
		if len(data) > remaining {
			data = data[:remaining]
		}
		_, _ = buffer.Buffer.Write(data)
	}
	return original, nil
}
