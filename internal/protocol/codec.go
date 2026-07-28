package protocol

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

var errEmptyPathPayload = errors.New("empty path payload")

type WireRecord struct {
	Kind    Kind
	Display string
	Payload string
}

func (r WireRecord) Bytes() []byte {
	record := make([]byte, 0, len(r.Kind)+len(r.Display)+len(r.Payload)+2)
	record = appendRecord(record, r)
	return record
}

func EncodePath(path []byte) string {
	return base64.StdEncoding.EncodeToString(path)
}

func DecodePath(payload string) ([]byte, error) {
	if payload == "" {
		return nil, errEmptyPathPayload
	}

	path, err := base64.StdEncoding.Strict().DecodeString(payload)
	if err != nil {
		return nil, fmt.Errorf("decode path payload: %w", err)
	}
	return path, nil
}

func EscapeDisplay(path []byte) string {
	var display strings.Builder
	display.Grow(len(path))

	for len(path) > 0 {
		switch path[0] {
		case '\\':
			display.WriteString(`\\`)
			path = path[1:]
			continue
		case '\'':
			display.WriteString(`\'`)
			path = path[1:]
			continue
		case '\t':
			display.WriteString(`\t`)
			path = path[1:]
			continue
		case '\n':
			display.WriteString(`\n`)
			path = path[1:]
			continue
		case '\r':
			display.WriteString(`\r`)
			path = path[1:]
			continue
		}

		r, size := utf8.DecodeRune(path)
		if r == utf8.RuneError && size == 1 {
			writeHexEscape(&display, path[0])
			path = path[1:]
			continue
		}
		if unicode.IsGraphic(r) {
			display.Write(path[:size])
		} else {
			for _, b := range path[:size] {
				writeHexEscape(&display, b)
			}
		}
		path = path[size:]
	}

	return display.String()
}

func ParseRecord(record []byte) (WireRecord, error) {
	if bytes.IndexByte(record, 0) >= 0 {
		return WireRecord{}, errors.New("record contains NUL")
	}
	if bytes.Count(record, []byte{'\t'}) != 2 {
		return WireRecord{}, errors.New("record must contain exactly two tabs")
	}

	firstTab := bytes.IndexByte(record, '\t')
	secondTab := firstTab + 1 + bytes.IndexByte(record[firstTab+1:], '\t')
	wire := WireRecord{
		Kind:    Kind(record[:firstTab]),
		Display: string(record[firstTab+1 : secondTab]),
		Payload: string(record[secondTab+1:]),
	}
	if !validKind(wire.Kind) {
		return WireRecord{}, fmt.Errorf("invalid record kind %q", wire.Kind)
	}
	path, err := DecodePath(wire.Payload)
	if err != nil {
		return WireRecord{}, err
	}
	if EncodePath(path) != wire.Payload {
		return WireRecord{}, errors.New("non-canonical path payload")
	}
	return wire, nil
}

func FrameRecords(records []WireRecord) []byte {
	size := len(records)
	for _, record := range records {
		size += len(record.Kind) + len(record.Display) + len(record.Payload) + 2
	}

	framed := make([]byte, 0, size)
	for _, record := range records {
		framed = appendRecord(framed, record)
		framed = append(framed, 0)
	}
	return framed
}

func WriteFramedRecords(w io.Writer, records []WireRecord) error {
	for _, record := range records {
		if err := writeAll(w, record.Bytes()); err != nil {
			return err
		}
		if err := writeAll(w, []byte{0}); err != nil {
			return err
		}
	}
	return nil
}

func appendRecord(dst []byte, record WireRecord) []byte {
	dst = append(dst, record.Kind...)
	dst = append(dst, '\t')
	dst = append(dst, record.Display...)
	dst = append(dst, '\t')
	dst = append(dst, record.Payload...)
	return dst
}

func validKind(kind Kind) bool {
	switch kind {
	case KindLocal, KindDirectory, KindFile, KindZoxide, KindDrive:
		return true
	default:
		return false
	}
}

func writeHexEscape(dst *strings.Builder, b byte) {
	const hex = "0123456789ABCDEF"
	dst.WriteString(`\x`)
	dst.WriteByte(hex[b>>4])
	dst.WriteByte(hex[b&0x0f])
}

func writeAll(w io.Writer, data []byte) error {
	if len(data) == 0 {
		return nil
	}
	n, err := w.Write(data)
	if n < 0 || n > len(data) {
		return fmt.Errorf("invalid write count %d", n)
	}
	if err != nil {
		return err
	}
	if n != len(data) {
		return io.ErrShortWrite
	}
	return nil
}
