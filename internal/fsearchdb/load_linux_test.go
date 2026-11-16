//go:build linux

package fsearchdb

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestLoadErrorsAndSavedAt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fsearch.db")
	data := fixture(binaryLittle, true, [][]byte{[]byte("/root")}, []uint32{0}, nil, 0)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	stamp := time.Unix(1700000000, 0)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	db, err := Load(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if !db.SavedAt.Equal(stamp) {
		t.Fatalf("SavedAt=%v, want %v", db.SavedAt, stamp)
	}
	if err := os.WriteFile(path, []byte("bad"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(context.Background(), path); !errors.Is(err, ErrMalformed) {
		t.Fatalf("got %v", err)
	}
	if _, err := Load(context.Background(), filepath.Join(dir, "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("got %v", err)
	}
	if _, err := Load(context.Background(), dir); !errors.Is(err, ErrMalformed) {
		t.Fatalf("directory load: %v", err)
	}
}

func TestLoadBusyAndCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fsearch.db")
	data := fixture(binaryLittle, true, [][]byte{[]byte("/root")}, []uint32{0}, nil, 0)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(context.Background(), path); !errors.Is(err, ErrBusy) {
		t.Fatalf("got %v", err)
	}
	_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Load(ctx, path); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

func TestLoadUsesOpenedSnapshotDuringReplacement(t *testing.T) {
	dir := t.TempDir()
	path, replacement := filepath.Join(dir, "db"), filepath.Join(dir, "new")
	old := fixture(binaryLittle, true, [][]byte{[]byte("/old")}, []uint32{0}, nil, 0)
	newData := fixture(binaryLittle, true, [][]byte{[]byte("/new")}, []uint32{0}, nil, 0)
	if err := os.WriteFile(path, old, 0600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(replacement, newData, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	oldBytes, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	oldDB, err := Decode(context.Background(), oldBytes)
	if err != nil {
		t.Fatal(err)
	}
	if string(oldDB.Entries[0].Path) != "/old" {
		t.Fatalf("opened descriptor changed to %q", oldDB.Entries[0].Path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	db, err := Decode(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	if string(db.Entries[0].Path) != "/new" {
		t.Fatal("replacement fixture invalid")
	}
	f.Close()
	got, err := Load(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Entries[0].Path) != "/new" {
		t.Fatalf("got %q", got.Entries[0].Path)
	}
	// Keeping an old descriptor alive across rename is what guarantees a
	// production Load already in progress cannot mix old/new file content.
}

var binaryLittle = binary.LittleEndian
