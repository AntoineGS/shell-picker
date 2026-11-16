package fsearchdb

import (
	"context"
	"crypto/md5"
	"encoding/binary"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestDecodeEntriesAndEndianness(t *testing.T) {
	for _, tt := range []struct {
		name   string
		order  binary.ByteOrder
		little bool
	}{
		{"little", binary.LittleEndian, true}, {"big", binary.BigEndian, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			data := fixture(tt.order, tt.little,
				[][]byte{[]byte("/root"), []byte("child"), []byte("/other")},
				[]uint32{0, 0, 2},
				[]testFile{{[]byte("weird \xff name"), 1}}, 1<<2|1<<3)
			db, err := Decode(context.Background(), data)
			if err != nil {
				t.Fatal(err)
			}
			want := [][]byte{[]byte("/root"), []byte("/root/child"), []byte("/other"), []byte("/root/child/weird \xff name")}
			if len(db.Entries) != len(want) {
				t.Fatalf("got %d entries", len(db.Entries))
			}
			for i := range want {
				if string(db.Entries[i].Path) != string(want[i]) {
					t.Errorf("entry %d path %q, want %q", i, db.Entries[i].Path, want[i])
				}
			}
			if !db.Entries[0].IsDir || db.Entries[3].IsDir {
				t.Fatal("directory/file type mismatch")
			}
			if db.Entries[0].Size != 1 || !db.Entries[0].ModTime.Equal(time.Unix(1700000000, 0)) {
				t.Fatalf("metadata missing: %+v", db.Entries[0])
			}
		})
	}
}

func TestDecodeRejectsInvalidData(t *testing.T) {
	valid := fixture(binary.LittleEndian, true, [][]byte{[]byte("/root")}, []uint32{0}, nil, 0)
	tests := []struct {
		name string
		data []byte
	}{
		{"bad checksum", append(append([]byte(nil), valid[:40]...), valid[40:]...)},
		{"truncated", valid[:len(valid)-2]},
		{"prefix", fixturePrefix()},
		{"cycle", fixture(binary.LittleEndian, true, [][]byte{[]byte("/root"), []byte("x")}, []uint32{1, 0}, nil, 0)},
		{"huge count", mutateFolderCount(valid, ^uint32(0))},
	}
	tests[0].data[7] ^= 1
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Decode(context.Background(), tt.data)
			if !errors.Is(err, ErrMalformed) {
				t.Fatalf("expected malformed, got %v", err)
			}
		})
	}
}

func TestDecodeCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Decode(ctx, fixture(binary.LittleEndian, true, [][]byte{[]byte("/root")}, []uint32{0}, nil, 0))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

func TestDecodeValidatesAndIgnoresSortedIndexTrailer(t *testing.T) {
	data := fixture(binary.LittleEndian, true, [][]byte{[]byte("/root")}, []uint32{0},
		[]testFile{{name: []byte("item"), parent: 0}}, 0)
	// Replace the zero sorted-array count with one NAME index and its one
	// folder/file permutation. These arrays are validated but never paths.
	data = append(data[:len(data)-4], 1, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)
	db, err := Decode(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	if len(db.Entries) != 2 || string(db.Entries[1].Path) != "/root/item" {
		t.Fatalf("unexpected entries from sorted-index fixture: %+v", db.Entries)
	}
}

func TestDecodeBoundsSharedExpandedNameBytes(t *testing.T) {
	data := fixture(binary.LittleEndian, true, [][]byte{[]byte("/root")}, []uint32{0},
		[]testFile{{name: []byte("abc"), parent: 0}, {name: []byte("abcd"), parent: 0}}, 0)
	if _, err := decodeWithLimits(context.Background(), data, 11); !errors.Is(err, ErrMalformed) {
		t.Fatalf("expected shared raw-name budget failure, got %v", err)
	}
	db, err := decodeWithLimits(context.Background(), data, 12)
	if err != nil {
		t.Fatalf("names exactly at budget should decode: %v", err)
	}
	if len(db.Entries) != 3 {
		t.Fatalf("got %d entries", len(db.Entries))
	}
}

func TestDecodeCancellationDuringParentReconstruction(t *testing.T) {
	const depth = 1025
	names := make([][]byte, depth)
	parents := make([]uint32, depth)
	for i := range names {
		names[i] = []byte(fmt.Sprintf("d%d", i))
		parents[i] = uint32(i + 1)
	}
	names[depth-1] = []byte("/root")
	parents[depth-1] = uint32(depth - 1)
	data := fixture(binary.LittleEndian, true, names, parents, nil, 0)
	// Parsing makes 10 header/config checks plus four checks per folder,
	// one folder-loop check and one sorted-trailer check. Reconstruction then
	// checks at the outer loop and at depth zero. Leave that check uncanceled;
	// cancel at the next periodic check after traversing 1024 parents.
	ctx := &cancelAfterErrCalls{cancelAt: 10 + depth*4 + 5}
	_, err := Decode(ctx, data)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation during reconstruction, got %v after %d checks", err, ctx.calls)
	}
	if ctx.calls != ctx.cancelAt {
		t.Fatalf("canceled at check %d, want reconstruction check %d", ctx.calls, ctx.cancelAt)
	}
}

func TestDecodeMtimePresenceAndIndependentProperties(t *testing.T) {
	tests := []struct {
		name       string
		flags      uint64
		mtimestamp int64
		wantSize   int64
		wantTime   time.Time
	}{
		{name: "absent", wantTime: time.Time{}},
		{name: "size only", flags: 1 << 2, wantSize: 1},
		{name: "mtime epoch", flags: 1 << 3, wantTime: time.Unix(0, 0)},
		{name: "mtime negative", flags: 1 << 3, mtimestamp: -2, wantTime: time.Unix(-2, 0)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := fixture(binary.LittleEndian, true, [][]byte{[]byte("/root")}, []uint32{0}, nil, tt.flags)
			if tt.flags&(1<<3) != 0 {
				// Root entry starts at byte 72: prefix/suffix/name, then mtime
				// (there is no size field in these cases).
				binary.LittleEndian.PutUint64(data[81:], uint64(tt.mtimestamp))
			}
			db, err := Decode(context.Background(), data)
			if err != nil {
				t.Fatal(err)
			}
			if db.Entries[0].Size != tt.wantSize {
				t.Errorf("size=%d, want %d", db.Entries[0].Size, tt.wantSize)
			}
			if !db.Entries[0].ModTime.Equal(tt.wantTime) {
				t.Errorf("mtime=%v, want %v", db.Entries[0].ModTime, tt.wantTime)
			}
		})
	}
}

func TestDecodeNonemptyIncludeExcludeMetadataAndForwardParent(t *testing.T) {
	data := fixtureWithConfig(binary.LittleEndian, true,
		[][]byte{[]byte("child"), []byte("/root")}, []uint32{1, 1},
		[]testFile{{name: []byte("leaf"), parent: 0}}, 0,
		[]string{"/configured/root"}, []string{"*.tmp"})
	db, err := Decode(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	if len(db.Entries) != 3 || string(db.Entries[0].Path) != "/root/child" || string(db.Entries[2].Path) != "/root/child/leaf" {
		t.Fatalf("unexpected forward-parent paths: %+v", db.Entries)
	}
}

type cancelAfterErrCalls struct {
	cancelAt int
	calls    int
}

func (c *cancelAfterErrCalls) Deadline() (time.Time, bool) { return time.Time{}, false }
func (c *cancelAfterErrCalls) Done() <-chan struct{}       { return nil }
func (c *cancelAfterErrCalls) Err() error {
	c.calls++
	if c.calls >= c.cancelAt {
		return context.Canceled
	}
	return nil
}
func (c *cancelAfterErrCalls) Value(any) any { return nil }

func fixturePrefix() []byte {
	data := fixture(binary.LittleEndian, true, [][]byte{[]byte("/root")}, []uint32{0}, nil, 0)
	// First folder name's prefix field follows metadata checksum.
	data[72], data[73] = 0xff, 0xff
	return data
}
func mutateFolderCount(data []byte, n uint32) []byte {
	out := append([]byte(nil), data...)
	// Empty config header ends at byte 32; folder count follows.
	binary.LittleEndian.PutUint32(out[32:], n)
	// Its checksum must remain valid to reach count validation.
	sum := md5.Sum(out[:56])
	copy(out[56:72], sum[:])
	return out
}
