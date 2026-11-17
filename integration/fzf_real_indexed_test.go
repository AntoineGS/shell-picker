package integration

import (
	"bytes"
	"crypto/md5"
	"encoding/binary"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestRealFZFSourceSwitchRestoresQueriesAndSourceMarks(t *testing.T) {
	fixture := newRealFZFFixture(t, requireRealFZF(t), "indexed source switch")
	alpha := filepath.Join(fixture.cwd, "alpha.txt")
	beta := filepath.Join(fixture.cwd, "beta.txt")
	for _, path := range []string{alpha, beta} {
		if err := os.WriteFile(path, []byte("indexed source test\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	databaseDir := filepath.Join(fixture.root, "xdg", "fsearch")
	if err := os.MkdirAll(databaseDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeSyntheticFSearchDB(t, filepath.Join(databaseDir, "fsearch.db"), fixture.cwd, []string{"alpha.txt", "beta.txt", "index-only.txt"})

	term := fixture.startSized(t, protocol.PickerCP,
		[]string{"XDG_DATA_HOME=" + filepath.Join(fixture.root, "xdg")}, 120, 35)
	defer term.Close()
	term.WaitBarrier(testContext(t), barrier{Event: "fzf.start", Count: 1})

	beforeInitialQuery := len(term.Output())
	if err := term.Send([]byte("alpha.txt")); err != nil {
		t.Fatal(err)
	}
	waitForTerminalTextAfter(t, term, beforeInitialQuery, "[I] alpha.txt")
	// This query uniquely matches the file. Waiting for the filtered match
	// count, not merely candidate text that may have been rendered at startup,
	// proves fzf has moved focus into the intended one-row result set.
	waitForTerminalTextAfter(t, term, beforeInitialQuery, "1/7")
	if err := term.Send(keyCtrlSpace); err != nil {
		t.Fatal(err)
	}
	waitForTerminalText(t, term, "(1)")
	term.WaitBarrier(testContext(t), barrier{Event: "callback.event", Operation: "mk", Count: 1})

	// Filter the marked browser row out before switching. The callback cache,
	// not fzf's visible-row selection file, must preserve the mark.
	query := "beta.txt"
	for range len("alpha.txt") {
		if err := term.Send([]byte{0x7f}); err != nil {
			t.Fatal(err)
		}
	}
	if err := term.Send([]byte(query)); err != nil {
		t.Fatal(err)
	}
	waitForTerminalText(t, term, query)

	sendAndWait(t, term, []byte{0x06}, barrier{Event: "callback.event", Operation: "sf", Count: 1})
	term.WaitBarrier(testContext(t), barrier{Event: "generation.publish", Generation: 2, Count: 1})
	term.WaitBarrier(testContext(t), barrier{Event: "callback.load", Generation: 2, Count: 1})
	waitForTerminalText(t, term, "FSearch · saved ")
	waitForTerminalText(t, term, query)

	beforeBrowser := len(term.Output())
	sendAndWait(t, term, []byte{0x06}, barrier{Event: "generation.publish", Generation: 3, Count: 1})
	term.WaitBarrier(testContext(t), barrier{Event: "callback.load", Generation: 3, Count: 1})
	waitForTerminalTextAfter(t, term, beforeBrowser, query)
	waitForTerminalTextAfter(t, term, beforeBrowser, "1/7")
	sendAndWait(t, term, keyEnter, barrier{Event: "callback.event", Operation: "en", Count: 1})
	if err := term.Wait(testContext(t)); err != nil {
		t.Fatal(err)
	}
	fixture.AssertAccepted(t, term, filepath.Base(alpha))
}

func TestRealFZFStandaloneSearchAcceptsMultiplePathsAfterFiltering(t *testing.T) {
	fzfPath := requireRealFZF(t)
	fixture := newRealFZFFixture(t, fzfPath, "standalone indexed search")
	databaseDir := filepath.Join(fixture.root, "xdg", "fsearch")
	if err := os.MkdirAll(databaseDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeSyntheticFSearchDB(t, filepath.Join(databaseDir, "fsearch.db"), fixture.cwd,
		[]string{"qz-first.txt", "qz-second.txt"})

	term := fixture.startSized(t, protocol.PickerSearch,
		[]string{"XDG_DATA_HOME=" + filepath.Join(fixture.root, "xdg")}, 120, 35)
	defer term.Close()
	term.WaitBarrier(testContext(t), barrier{Event: "fzf.start", Count: 1})
	waitForTerminalText(t, term, "FSearch")

	for _, name := range []string{"qz-first.txt", "qz-second.txt"} {
		beforeQuery := len(term.Output())
		if name == "qz-second.txt" {
			for range len("qz-first.txt") {
				if err := term.Send([]byte{0x7f}); err != nil {
					t.Fatal(err)
				}
			}
			if err := term.Send([]byte(name)); err != nil {
				t.Fatal(err)
			}
		} else if err := term.Send([]byte(name)); err != nil {
			t.Fatal(err)
		}
		waitForTerminalTextAfter(t, term, beforeQuery, name)
		if err := term.Send(keyCtrlSpace); err != nil {
			t.Fatal(err)
		}
		term.WaitBarrier(testContext(t), barrier{Event: "callback.event", Operation: "mk",
			Count: map[string]int{"qz-first.txt": 1, "qz-second.txt": 2}[name]})
	}
	beforeUnmark := len(term.Output())
	if err := term.Send([]byte("\x1b[Z")); err != nil {
		t.Fatal(err)
	}
	term.WaitBarrier(testContext(t), barrier{Event: "callback.event", Operation: "mk", Count: 3})
	waitForTerminalTextAfter(t, term, beforeUnmark, "(1)")
	// Keep the authoritative cache while filtering every selected row out of
	// the visible result set. Acceptance must not depend on a focused row or
	// fzf's selected-file output.
	for range len("qz-second.txt") {
		if err := term.Send([]byte{0x7f}); err != nil {
			t.Fatal(err)
		}
	}
	if err := term.Send([]byte("no-visible-row")); err != nil {
		t.Fatal(err)
	}
	waitForTerminalText(t, term, "no-visible-row")
	if err := term.Send(keyEnter); err != nil {
		t.Fatal(err)
	}
	if err := term.Wait(testContext(t)); err != nil {
		t.Fatalf("wait for standalone search acceptance: %v; events=%+v; output=%q",
			err, term.TraceEvents(), term.Output())
	}
	fixture.AssertAccepted(t, term, filepath.Join(fixture.cwd, "qz-first.txt"))
}

func TestRealFZFCDSpaceUsesAuthoritativeMarkedDirectory(t *testing.T) {
	fixture := newRealFZFFixture(t, requireRealFZF(t), "cd authoritative selection")
	term := fixture.Start(t, protocol.PickerCD)
	defer term.Close()
	term.WaitBarrier(testContext(t), barrier{Event: "fzf.start", Count: 1})

	beforeQuery := len(term.Output())
	if err := term.Send([]byte("alpha")); err != nil {
		t.Fatal(err)
	}
	waitForTerminalTextAfter(t, term, beforeQuery, "[I] alpha")
	waitForTerminalTextAfter(t, term, beforeQuery, "1/5")
	sendAndWait(t, term, keyEsc, barrier{Event: "callback.event", Operation: "es", Count: 1})
	waitForTerminalText(t, term, "[N]")
	// Normal mode disables filtering; from the top entries (. and ..), alpha
	// is row 3 and visible is row 4 in the sorted browser list.
	for range 2 {
		if err := term.Send(keyDown); err != nil {
			t.Fatal(err)
		}
	}
	sendAndWait(t, term, keySpace, barrier{Event: "callback.event", Operation: "mk", Count: 1})
	if err := term.Send(keyDown); err != nil {
		t.Fatal(err)
	}
	sendAndWait(t, term, keySpace, barrier{Event: "callback.event", Operation: "mk", Count: 2})
	// Unmark the replacement and accept with an empty authoritative cache.
	// If native --multi=1 still holds alpha, fzf outputs alpha instead of the
	// focused visible row.
	sendAndWait(t, term, keySpace, barrier{Event: "callback.event", Operation: "mk", Count: 3})
	sendAndWait(t, term, keyEnter, barrier{Event: "callback.event", Operation: "en", Count: 1})
	if err := term.Wait(testContext(t)); err != nil {
		t.Fatalf("wait for cd acceptance: %v; events=%+v; output=%q",
			err, term.TraceEvents(), term.Output())
	}
	fixture.AssertAccepted(t, term, filepath.Join(fixture.cwd, "visible"))
}

func writeSyntheticFSearchDB(t *testing.T, path, root string, names []string) {
	t.Helper()
	sort.Strings(names)
	var header, folders, files bytes.Buffer
	header.WriteString("FSDB")
	header.Write([]byte{6, 0, 1})
	put := func(writer *bytes.Buffer, value any) {
		if err := binary.Write(writer, binary.LittleEndian, value); err != nil {
			t.Fatal(err)
		}
	}
	put(&header, uint64(0)) // layout flags
	put(&header, uint64(0)) // sort flags
	put(&header, uint32(0)) // include rules
	put(&header, uint32(0)) // exclude rules
	header.WriteByte(1)     // hidden entries included
	put(&folders, uint16(0))
	put(&folders, uint16(len(root)))
	folders.WriteString(root)
	put(&folders, uint32(0)) // root parent points to itself
	var previous []byte
	for _, name := range names {
		current := []byte(name)
		prefix := 0
		for prefix < len(previous) && prefix < len(current) && previous[prefix] == current[prefix] {
			prefix++
		}
		put(&files, uint16(prefix))
		put(&files, uint16(len(current)-prefix))
		files.Write(current[prefix:])
		put(&files, uint32(0))
		previous = current
	}
	put(&header, uint32(1))
	put(&header, uint32(len(names)))
	put(&header, uint64(folders.Len()))
	put(&header, uint64(files.Len()))
	checksum := md5.Sum(header.Bytes())
	header.Write(checksum[:])
	header.Write(folders.Bytes())
	header.Write(files.Bytes())
	put(&header, uint32(0)) // no sorted index arrays
	if err := os.WriteFile(path, header.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}
