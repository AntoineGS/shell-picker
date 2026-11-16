package fsearchdb

import (
	"bytes"
	"fmt"
	"math"
)

// readEntries decodes one prefix-compressed block of folders or files.
func readEntries(d *decoder, end int, count uint32, flags uint64, expandedNameBytes *uint64, nameBudget uint64) ([]rawEntry, error) {
	out := make([]rawEntry, 0, count)
	var previous []byte
	for i := uint32(0); i < count; i++ {
		if d.pos >= end {
			return nil, fmt.Errorf("%w: entry block truncated", ErrMalformed)
		}
		off, err := d.u16()
		if err != nil {
			return nil, err
		}
		n, err := d.u16()
		if err != nil {
			return nil, err
		}
		if uint32(n) > maxEntryName || int(off) > len(previous) {
			return nil, fmt.Errorf("%w: invalid compressed name prefix", ErrMalformed)
		}
		expandedLen := uint64(off) + uint64(n)
		if expandedLen > nameBudget-*expandedNameBytes {
			return nil, fmt.Errorf("%w: aggregate expanded names exceed limit", ErrMalformed)
		}
		*expandedNameBytes += expandedLen
		suffix, err := d.take(uint64(n))
		if err != nil {
			return nil, err
		}
		name := make([]byte, int(off)+len(suffix))
		copy(name, previous[:off])
		copy(name[off:], suffix)
		if bytes.IndexByte(name, 0) >= 0 {
			return nil, fmt.Errorf("%w: NUL in entry name", ErrMalformed)
		}
		previous = name
		entry := rawEntry{name: name}
		if flags&(1<<2) != 0 {
			v, e := d.u64()
			if e != nil {
				return nil, e
			}
			if v > math.MaxInt64 {
				return nil, fmt.Errorf("%w: entry size exceeds supported range", ErrMalformed)
			}
			entry.size = int64(v)
		}
		if flags&(1<<3) != 0 {
			v, e := d.u64()
			if e != nil {
				return nil, e
			}
			entry.mtime = int64(v)
			entry.hasMTime = true
		}
		entry.parent, err = d.u32()
		if err != nil {
			return nil, err
		}
		if d.pos > end {
			return nil, fmt.Errorf("%w: entry exceeds block boundary", ErrMalformed)
		}
		out = append(out, entry)
		if i&0x3fff == 0 {
			if err := d.ctx.Err(); err != nil {
				return nil, err
			}
		}
	}
	if d.pos != end {
		return nil, fmt.Errorf("%w: entry block size mismatch", ErrMalformed)
	}
	return out, nil
}

// validateSorted verifies FSearch's index trailer without using it to derive paths.
func validateSorted(d *decoder, nf, nfiles uint32) error {
	count, err := d.u32()
	if err != nil {
		return err
	}
	if count > 10 {
		return fmt.Errorf("%w: unreasonable sorted-index count", ErrMalformed)
	}
	seen := make(map[uint32]bool, count)
	for i := uint32(0); i < count; i++ {
		id, err := d.u32()
		if err != nil {
			return err
		}
		if id == 0 || id >= 13 || seen[id] {
			return fmt.Errorf("%w: invalid sorted-index id", ErrMalformed)
		}
		seen[id] = true
		for _, n := range []uint32{nf, nfiles} {
			bits := make([]uint64, (uint64(n)+63)/64)
			for j := uint32(0); j < n; j++ {
				v, e := d.u32()
				if e != nil {
					return e
				}
				if v >= n {
					return fmt.Errorf("%w: sorted-index value out of range", ErrMalformed)
				}
				mask := uint64(1) << (v % 64)
				word := v / 64
				if bits[word]&mask != 0 {
					return fmt.Errorf("%w: duplicate sorted-index value", ErrMalformed)
				}
				bits[word] |= mask
			}
		}
	}
	return nil
}
