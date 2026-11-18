//go:build linux

package app

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/AntoineGS/shell-picker/internal/fsearchdb"
	"github.com/AntoineGS/shell-picker/internal/sessionipc"
)

const (
	unearthMaxRecordBytes = 1 << 20
	unearthMaxEntries     = 20_000_000
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
	command := exec.CommandContext(ctx, executable, "--require-live-index", "--index-binary", "*", root)
	command.Env = environment
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("unearth stdout: %w", err)
	}
	var stderr limitedBuffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("start unearth: %w", err)
	}

	database, decodeErr := decodeUnearth(stdout)
	if decodeErr != nil {
		_ = command.Process.Kill()
	}
	waitErr := command.Wait()
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

func decodeUnearth(reader io.Reader) (*fsearchdb.Database, error) {
	database := &fsearchdb.Database{Entries: make([]fsearchdb.Entry, 0)}
	var total uint64
	for {
		var sizeBytes [4]byte
		_, err := io.ReadFull(reader, sizeBytes[:])
		if errors.Is(err, io.EOF) {
			return database, nil
		}
		if err != nil {
			return nil, fmt.Errorf("read record size: %w", err)
		}
		size := binary.LittleEndian.Uint32(sizeBytes[:])
		if size == 0 {
			return nil, errors.New("zero-length path record")
		}
		if size > unearthMaxRecordBytes {
			return nil, fmt.Errorf("path record size %d exceeds %d-byte limit", size, unearthMaxRecordBytes)
		}
		if len(database.Entries) >= unearthMaxEntries {
			return nil, fmt.Errorf("path count exceeds %d-entry limit", unearthMaxEntries)
		}
		path := make([]byte, int(size))
		if _, err := io.ReadFull(reader, path); err != nil {
			return nil, fmt.Errorf("read path record: %w", err)
		}
		if path[0] != '/' || bytes.IndexByte(path, 0) >= 0 {
			return nil, fmt.Errorf("invalid unearth path record")
		}
		total += uint64(len(path))
		if total > uint64(sessionipc.MaxLoadResponseBytes) {
			return nil, fmt.Errorf("unearth paths exceed %d-byte limit", sessionipc.MaxLoadResponseBytes)
		}
		isDir := path[len(path)-1] == '/'
		if isDir && len(path) > 1 {
			path = path[:len(path)-1]
		}
		database.Entries = append(database.Entries, fsearchdb.Entry{Path: path, IsDir: isDir})
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
