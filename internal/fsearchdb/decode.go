package fsearchdb

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/binary"
	"fmt"
	"path/filepath"
	"time"
)

const knownFlags uint64 = (1 << 11) - 1
const maxExpandedNameBytes uint64 = 1 << 30

type decoder struct {
	ctx   context.Context
	data  []byte
	pos   int
	order binary.ByteOrder
}

func (d *decoder) take(n uint64) ([]byte, error) {
	if err := d.ctx.Err(); err != nil {
		return nil, err
	}
	if n > uint64(len(d.data)-d.pos) {
		return nil, fmt.Errorf("%w: truncated field at byte %d", ErrMalformed, d.pos)
	}
	b := d.data[d.pos : d.pos+int(n)]
	d.pos += int(n)
	return b, nil
}
func (d *decoder) u8() (uint8, error) {
	b, err := d.take(1)
	if err != nil {
		return 0, err
	}
	return b[0], nil
}
func (d *decoder) u16() (uint16, error) {
	b, err := d.take(2)
	if err != nil {
		return 0, err
	}
	return d.order.Uint16(b), nil
}
func (d *decoder) u32() (uint32, error) {
	b, err := d.take(4)
	if err != nil {
		return 0, err
	}
	return d.order.Uint32(b), nil
}
func (d *decoder) u64() (uint64, error) {
	b, err := d.take(8)
	if err != nil {
		return 0, err
	}
	return d.order.Uint64(b), nil
}
func (d *decoder) string(limit uint32) ([]byte, error) {
	n, err := d.u32()
	if err != nil {
		return nil, err
	}
	if n > limit {
		return nil, fmt.Errorf("%w: string length %d exceeds limit", ErrMalformed, n)
	}
	b, err := d.take(uint64(n))
	if err != nil {
		return nil, err
	}
	if bytes.IndexByte(b, 0) >= 0 {
		return nil, fmt.Errorf("%w: embedded NUL in metadata string", ErrMalformed)
	}
	return b, nil
}

// Decode reads a version 6.0 saved database. It does not set Database.SavedAt.
func Decode(ctx context.Context, data []byte) (*Database, error) {
	return decodeWithLimits(ctx, data, maxExpandedNameBytes)
}

// decodeWithLimits keeps the raw-name allocation budget injectable for small,
// deterministic regression tests. Public callers use the fixed production cap.
func decodeWithLimits(ctx context.Context, data []byte, nameBudget uint64) (*Database, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: nil context", ErrMalformed)
	}
	if len(data) < 7 {
		return nil, fmt.Errorf("%w: truncated header", ErrMalformed)
	}
	if !bytes.Equal(data[:4], []byte("FSDB")) {
		return nil, fmt.Errorf("%w: invalid magic", ErrMalformed)
	}
	if data[4] != 6 || data[5] > 0 {
		return nil, fmt.Errorf("%w: version %d.%d (supported: 6.0)", ErrUnsupported, data[4], data[5])
	}
	var order binary.ByteOrder
	switch data[6] {
	case 1:
		order = binary.LittleEndian
	case 0:
		order = binary.BigEndian
	default:
		return nil, fmt.Errorf("%w: invalid endianness marker %d", ErrMalformed, data[6])
	}
	d := &decoder{ctx: ctx, data: data, pos: 7, order: order}
	flags, err := d.u64()
	if err != nil {
		return nil, err
	}
	sortFlags, err := d.u64()
	if err != nil {
		return nil, err
	}
	if flags&^knownFlags != 0 || sortFlags&^knownFlags != 0 {
		return nil, fmt.Errorf("%w: unknown index flags", ErrUnsupported)
	}
	if err := parseIncludes(d); err != nil {
		return nil, err
	}
	if err := parseExcludes(d); err != nil {
		return nil, err
	}
	nFolders, err := d.u32()
	if err != nil {
		return nil, err
	}
	nFiles, err := d.u32()
	if err != nil {
		return nil, err
	}
	if uint64(nFolders)+uint64(nFiles) > maxEntries || nFolders == 0 {
		return nil, fmt.Errorf("%w: unreasonable entry count", ErrMalformed)
	}
	folderSize, err := d.u64()
	if err != nil {
		return nil, err
	}
	fileSize, err := d.u64()
	if err != nil {
		return nil, err
	}
	checksumAt := d.pos
	stored, err := d.take(16)
	if err != nil {
		return nil, err
	}
	// The format checksum covers bytes before these 16 bytes and the two block
	// lengths, but deliberately excludes the (potentially enormous) entry blocks.
	sum := md5.Sum(data[:checksumAt])
	if !bytes.Equal(stored, sum[:]) {
		return nil, fmt.Errorf("%w: metadata checksum mismatch", ErrMalformed)
	}
	if folderSize > uint64(len(data)-d.pos) || fileSize > uint64(len(data)-d.pos)-folderSize {
		return nil, fmt.Errorf("%w: entry blocks exceed file bounds", ErrMalformed)
	}
	if uint64(nFolders) > folderSize/8 || uint64(nFiles) > fileSize/8 {
		return nil, fmt.Errorf("%w: entry count cannot fit in declared block", ErrMalformed)
	}
	folderEnd := d.pos + int(folderSize)
	fileEnd := folderEnd + int(fileSize)
	var expandedNameBytes uint64
	folders, err := readEntries(d, folderEnd, nFolders, flags, &expandedNameBytes, nameBudget)
	if err != nil {
		return nil, err
	}
	files, err := readEntries(d, fileEnd, nFiles, flags, &expandedNameBytes, nameBudget)
	if err != nil {
		return nil, err
	}
	if d.pos != fileEnd {
		return nil, fmt.Errorf("%w: entry block size mismatch", ErrMalformed)
	}
	for i, folder := range folders {
		if folder.parent != ^uint32(0) && folder.parent != uint32(i) && folder.parent >= nFolders {
			return nil, fmt.Errorf("%w: folder parent index out of range", ErrMalformed)
		}
	}
	for _, file := range files {
		if file.parent >= nFolders {
			return nil, fmt.Errorf("%w: file parent index out of range", ErrMalformed)
		}
	}
	if err := validateSorted(d, nFolders, nFiles); err != nil {
		return nil, err
	}
	if d.pos != len(data) {
		return nil, fmt.Errorf("%w: trailing bytes", ErrMalformed)
	}
	entries, err := makePaths(ctx, folders, files)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &Database{Entries: entries}, nil
}

func parseIncludes(d *decoder) error {
	n, err := d.u32()
	if err != nil {
		return err
	}
	if n > maxConfigItems {
		return fmt.Errorf("%w: excessive include count", ErrMalformed)
	}
	for i := uint32(0); i < n; i++ {
		typ, e := d.u32()
		if e != nil {
			return e
		}
		if typ != 0 {
			return fmt.Errorf("%w: include type %d", ErrUnsupported, typ)
		}
		if _, e = d.string(4 * 4096); e != nil {
			return e
		}
		if _, e = d.take(4 + 8 + 4 + 8 + 4 + 4 + 4 + 1); e != nil {
			return e
		}
	}
	return nil
}
func parseExcludes(d *decoder) error {
	n, err := d.u32()
	if err != nil {
		return err
	}
	if n > maxConfigItems {
		return fmt.Errorf("%w: excessive exclude count", ErrMalformed)
	}
	for i := uint32(0); i < n; i++ {
		typ, e := d.u32()
		if e != nil {
			return e
		}
		if typ != 0 {
			return fmt.Errorf("%w: exclude record type %d", ErrUnsupported, typ)
		}
		if _, e = d.take(3); e != nil {
			return e
		}
		if _, e = d.string(4 * 4096); e != nil {
			return e
		}
		if _, e = d.u8(); e != nil {
			return e
		}
	}
	_, err = d.u8()
	return err
}

func unixTime(v int64, present bool) time.Time {
	if !present {
		return time.Time{}
	}
	return time.Unix(v, 0)
}

// DefaultPath chooses the configured XDG data home only when it is absolute.
func DefaultPath(home string, lookup func(string) string) string {
	if lookup != nil {
		if xdg := lookup("XDG_DATA_HOME"); filepath.IsAbs(xdg) {
			return filepath.Join(xdg, "fsearch", "fsearch.db")
		}
	}
	return filepath.Join(home, ".local", "share", "fsearch", "fsearch.db")
}
