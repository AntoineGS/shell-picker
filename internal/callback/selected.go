package callback

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/AntoineGS/shell-picker/internal/protocol"
)

const maxFZFSelectionFile = 64 << 20
const maxFZFSelectedRecords = 1_000_000

// ParseSelectedFileCommand accepts the one fzf-only extension used to carry
// authenticated selected rows without interpolating candidate contents.
func ParseSelectedFileCommand(raw, path string) (Command, error) {
	command, err := Parse(raw)
	if err != nil || raw != "e:sf" {
		return Command{}, ErrGrammar
	}
	if strings.HasPrefix(path, "'") && strings.HasSuffix(path, "'") && len(path) >= 2 {
		path = path[1 : len(path)-1]
		if strings.ContainsAny(path, "'\r\n`;$|&<>") {
			return Command{}, ErrGrammar
		}
	}
	command.SelectedRecords, err = readSelectedRecords(path)
	if err != nil {
		return Command{}, err
	}
	return command, nil
}

// ParseSelectedFileArgument parses the complete command string passed by fzf's
// --with-shell callback. It accepts only the fixed callback and a plain path.
func ParseSelectedFileArgument(raw string) (Command, error) {
	const prefix = "e:sf --selected-file "
	if !strings.HasPrefix(raw, prefix) {
		return Command{}, ErrGrammar
	}
	path := strings.TrimPrefix(raw, prefix)
	if strings.HasPrefix(path, "'") && strings.HasSuffix(path, "'") && len(path) >= 2 {
		path = path[1 : len(path)-1]
		if strings.ContainsAny(path, "'\r\n`;$|&<>") {
			return Command{}, ErrGrammar
		}
	} else if strings.ContainsAny(path, " \t\r\n\"'`;$|&<>") {
		return Command{}, ErrGrammar
	}
	if path == "" {
		return Command{}, ErrGrammar
	}
	return ParseSelectedFileCommand("e:sf", path)
}

func readSelectedRecords(path string) ([][]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("callback: selected-record file path must be absolute")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("callback: selected-record file unavailable")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maxFZFSelectionFile {
		return nil, errors.New("callback: invalid selected-record file")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxFZFSelectionFile+1))
	if err != nil || len(raw) > maxFZFSelectionFile {
		return nil, errors.New("callback: selected-record file exceeds limit")
	}
	if len(raw) == 0 || bytes.Equal(raw, []byte{0}) {
		return nil, nil
	}
	delimiter := byte('\n')
	if bytes.Contains(raw, []byte{0}) {
		delimiter = 0
	}
	raw = bytes.TrimSuffix(raw, []byte{delimiter})
	lines := bytes.Split(raw, []byte{delimiter})
	if len(lines) > maxFZFSelectedRecords {
		return nil, errors.New("callback: too many selected records")
	}
	records := make([][]byte, len(lines))
	for index, line := range lines {
		if len(line) == 0 {
			return nil, errors.New("callback: empty selected-record row")
		}
		if _, err := protocol.ParseRecord(line); err != nil {
			return nil, errors.New("callback: malformed selected-record row")
		}
		records[index] = bytes.Clone(line)
	}
	return records, nil
}

func encodeSelectedRecords(records [][]byte) (string, error) {
	if len(records) == 0 {
		return "", nil
	}
	var framed bytes.Buffer
	for _, record := range records {
		if len(record) == 0 || bytes.IndexByte(record, 0) >= 0 ||
			framed.Len() > maxFZFSelectionFile-len(record)-1 {
			return "", errors.New("callback: selected-record data exceeds limit")
		}
		framed.Write(record)
		framed.WriteByte(0)
	}
	var compressed bytes.Buffer
	writer, err := gzip.NewWriterLevel(&compressed, gzip.BestSpeed)
	if err != nil {
		return "", errors.New("callback: compress selected records")
	}
	if _, err := writer.Write(framed.Bytes()); err != nil {
		return "", errors.New("callback: compress selected records")
	}
	if err := writer.Close(); err != nil {
		return "", errors.New("callback: finish selected-record compression")
	}
	return base64.StdEncoding.EncodeToString(compressed.Bytes()), nil
}
