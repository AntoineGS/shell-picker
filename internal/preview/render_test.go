package preview

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	processpkg "github.com/AntoineGS/shell-picker/internal/process"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestCorePreviewEveryCategoryHasBoundedNativeFallback(t *testing.T) {
	dir := t.TempDir()
	fixtures := writeFixtures(t, dir)
	cases := map[string]Category{
		"directory": CategoryDirectory, "readme.md": CategoryMarkdown, "plain.txt": CategoryText,
		"image.png": CategoryImage, "document.pdf": CategoryPDF, "video.mp4": CategoryVideo,
		"audio.mp3": CategoryAudio, "sample.zip": CategoryZip, "sample.gz": CategoryGzip,
		"sample.xz": CategoryXz, "sample.tar": CategoryTar, "sample.bz2": CategoryBzip,
		"binary.bin": CategoryBinary,
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			var output bytes.Buffer
			starts := 0
			options := testOptions(&output)
			options.Runner.Observe = func(event processpkg.ProcessEvent) {
				if event.Phase == "start" {
					starts++
				}
			}
			if err := Render(context.Background(), resolved(fixtures[name]), options); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(fixtures[name])
			if err != nil {
				t.Fatal(err)
			}
			prefix := []byte(nil)
			if !info.IsDir() {
				prefix, err = os.ReadFile(fixtures[name])
				if err != nil {
					t.Fatal(err)
				}
			}
			got, err := Detect(prefix, info)
			if err != nil || got != want || output.Len() == 0 || output.Len() > 4<<20 || starts != 0 {
				t.Fatalf("category=%q output=%d starts=%d err=%v", got, output.Len(), starts, err)
			}
		})
	}
}

func TestPreviewRejectsRelativeAndVirtualBeforeFilesystemWork(t *testing.T) {
	for _, candidate := range []protocol.ResolvedCandidate{
		{Kind: protocol.KindFile, Path: []byte("relative")},
		{Kind: protocol.KindVirtual, Path: []byte(filepath.Join(t.TempDir(), "missing"))},
	} {
		var output bytes.Buffer
		options := testOptions(&output)
		options.Runner.Observe = func(processpkg.ProcessEvent) { t.Fatal("started child") }
		err := Render(context.Background(), candidate, options)
		if !errors.Is(err, ErrPathNotAbsolute) || output.Len() != 0 {
			t.Fatalf("err=%v output=%q", err, output.String())
		}
	}
}

func TestPreviewOutputAndDeadlineAreHardLimits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat("line\n", 100)), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	options := testOptions(&output)
	options.Limits.MaxOutputBytes = 32
	if err := Render(context.Background(), resolved(path), options); !errors.Is(err, ErrOutputLimit) {
		t.Fatalf("output limit err=%v", err)
	}
	if output.Len() > 32 {
		t.Fatalf("wrote %d bytes", output.Len())
	}

	output.Reset()
	options = testOptions(&output)
	options.Limits.Deadline = time.Nanosecond
	err := Render(context.Background(), resolved(path), options)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline err=%v", err)
	}
}

func TestPreviewUsesContainedOptionalRendererWhenPresent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-specific")
	}
	tools := t.TempDir()
	bat := filepath.Join(tools, "bat")
	if err := os.WriteFile(bat, []byte("#!/bin/sh\nprintf 'external renderer\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "plain.txt")
	if err := os.WriteFile(path, []byte("native text\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	options := testOptions(&output)
	options.Environment = []string{"PATH=" + tools}
	starts := 0
	options.Runner.Observe = func(event processpkg.ProcessEvent) {
		if event.Phase == "start" {
			starts++
		}
	}
	if err := Render(context.Background(), resolved(path), options); err != nil {
		t.Fatal(err)
	}
	if output.String() != "external renderer\n" || starts != 1 {
		t.Fatalf("output=%q starts=%d", output.String(), starts)
	}
}

func testOptions(output *bytes.Buffer) Options {
	return Options{Columns: 80, Lines: 40, Environment: []string{"PATH="}, Runner: processpkg.Runner{},
		Limits: DefaultLimits, Stdout: output, Stderr: output}
}

func resolved(path string) protocol.ResolvedCandidate {
	return protocol.ResolvedCandidate{Kind: protocol.KindFile, Path: []byte(path)}
}
