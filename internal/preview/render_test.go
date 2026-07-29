package preview

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
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

func TestPreviewLongPermittedTextLineEmitsUsefulFallback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "long.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 2<<20)), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := Render(context.Background(), resolved(path), testOptions(&output)); err != nil {
		t.Fatal(err)
	}
	if output.Len() == 0 || !bytes.HasPrefix(output.Bytes(), []byte("     1  ")) {
		t.Fatalf("output bytes=%d prefix=%q", output.Len(), output.Bytes()[:min(output.Len(), 16)])
	}
}

func TestPreviewExactInputLimitTextLineEmitsOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "exact-limit.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", int(DefaultLimits.MaxInternalInputBytes))), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := Render(context.Background(), resolved(path), testOptions(&output)); err != nil {
		t.Fatal(err)
	}
	if output.Len() == 0 {
		t.Fatal("blank exact-limit text preview")
	}
}

func TestPreviewExternalStreamsStaySeparateUnderOneBudget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-specific")
	}
	tools := t.TempDir()
	if err := os.WriteFile(filepath.Join(tools, "bat"), []byte("#!/bin/sh\nprintf stdout\nprintf stderr >&2\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "plain.txt")
	if err := os.WriteFile(path, []byte("native\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	options := testOptions(&stdout)
	options.Stderr = &stderr
	options.Environment = []string{"PATH=" + tools}
	if err := Render(context.Background(), resolved(path), options); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "stdout" || stderr.String() != "stderr" {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestPreviewStderrOnlyExternalSuccessFallsBackToNativeStdout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-specific")
	}
	tools := t.TempDir()
	if err := os.WriteFile(filepath.Join(tools, "bat"), []byte("#!/bin/sh\nprintf diagnostic >&2\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "plain.txt")
	if err := os.WriteFile(path, []byte("native output\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	options := testOptions(&stdout)
	options.Stderr = &stderr
	options.Environment = []string{"PATH=" + tools}
	if err := Render(context.Background(), resolved(path), options); err != nil {
		t.Fatal(err)
	}
	if stdout.Len() == 0 || !strings.Contains(stdout.String(), "native output") || stderr.String() != "diagnostic" {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestTextRendererFallbackOrder(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("direct executable fixtures are Unix-specific")
	}
	t.Run("markdown prefers glow with exact arguments", func(t *testing.T) {
		tools, log := t.TempDir(), filepath.Join(t.TempDir(), "calls")
		installPreviewTool(t, tools, "glow", log, 0)
		installPreviewTool(t, tools, "bat", log, 0)
		path := filepath.Join(t.TempDir(), "read me.md")
		if err := os.WriteFile(path, []byte("# title\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		var output bytes.Buffer
		options := testOptions(&output)
		options.Environment = []string{"PATH=" + tools}
		if err := Render(context.Background(), resolved(path), options); err != nil {
			t.Fatal(err)
		}
		assertToolLog(t, log, []string{filepath.Join(tools, "glow"), "--width", "79", path})
	})
	t.Run("directory prefers eza", func(t *testing.T) {
		tools, log := t.TempDir(), filepath.Join(t.TempDir(), "calls")
		installPreviewTool(t, tools, "eza", log, 0)
		path := t.TempDir()
		var output bytes.Buffer
		options := testOptions(&output)
		options.Environment = []string{"PATH=" + tools}
		if err := Render(context.Background(), resolved(path), options); err != nil {
			t.Fatal(err)
		}
		assertToolLog(t, log, []string{filepath.Join(tools, "eza"), "--color=always", "--icons=always", "--group-directories-first", "--", path})
	})
	t.Run("plain text has numbered native fallback", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "plain.txt")
		if err := os.WriteFile(path, []byte("one\ntwo\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		var output bytes.Buffer
		if err := Render(context.Background(), resolved(path), testOptions(&output)); err != nil {
			t.Fatal(err)
		}
		if output.String() != "     1  one\n     2  two\n" {
			t.Fatalf("output=%q", output.String())
		}
	})
}

func TestOptionalRendererPrecedenceAndArguments(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("direct executable fixtures are Unix-specific")
	}
	fixtures := writeFixtures(t, t.TempDir())
	cases := []struct {
		name, fixture, tool, term string
		arguments                 func(string) []string
	}{
		{name: "qualified kitten", fixture: "image.png", tool: "kitten", term: "xterm-kitty", arguments: func(path string) []string {
			return []string{"icat", "--clear", "--transfer-mode=memory", "--place", "80x40@0x0", "--", path}
		}},
		{name: "chafa", fixture: "image.png", tool: "chafa", arguments: func(path string) []string {
			return []string{"--size", "80x40", "--", path}
		}},
		{name: "audio metadata", fixture: "audio.mp3", tool: "exiftool", arguments: afterDoubleDash},
		{name: "zip listing", fixture: "sample.zip", tool: "unzip", arguments: func(path string) []string { return []string{"-l", "--", path} }},
		{name: "gzip listing", fixture: "sample.gz", tool: "gzip", arguments: func(path string) []string { return []string{"--list", "--", path} }},
		{name: "xz listing", fixture: "sample.xz", tool: "xz", arguments: func(path string) []string { return []string{"--list", "--", path} }},
		{name: "tar listing", fixture: "sample.tar", tool: "tar", arguments: func(path string) []string { return []string{"--list", "--file", path} }},
		{name: "bzip check", fixture: "sample.bz2", tool: "bzip2", arguments: func(path string) []string { return []string{"--test", "--verbose", "--", path} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tools, log := t.TempDir(), filepath.Join(t.TempDir(), "call")
			installPreviewTool(t, tools, tc.tool, log, 0)
			var output bytes.Buffer
			options := testOptions(&output)
			options.Environment = []string{"PATH=" + tools, "TERM=" + tc.term}
			if err := Render(context.Background(), resolved(fixtures[tc.fixture]), options); err != nil {
				t.Fatal(err)
			}
			assertToolLog(t, log, append([]string{filepath.Join(tools, tc.tool)}, tc.arguments(fixtures[tc.fixture])...))
		})
	}
}

func TestRendererFailuresAreWaitedSequentiallyBeforeNativeFallback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("direct executable fixtures are Unix-specific")
	}
	tools := t.TempDir()
	installPreviewTool(t, tools, "kitten", filepath.Join(t.TempDir(), "kitten"), 1)
	installPreviewTool(t, tools, "chafa", filepath.Join(t.TempDir(), "chafa"), 1)
	path := writeFixtures(t, t.TempDir())["image.png"]
	var output bytes.Buffer
	options := testOptions(&output)
	options.Environment = []string{"PATH=" + tools, "TERM=xterm-kitty"}
	starts, live, maximum, dispatches := 0, 0, 0, 0
	options.Runner.Observe = func(event processpkg.ProcessEvent) {
		switch event.Phase {
		case "start":
			starts++
			live++
			maximum = max(maximum, live)
		case "exit":
			live--
		}
	}
	options.OnDispatch = func(string, int, time.Duration) { dispatches++ }
	if err := Render(context.Background(), resolved(path), options); err != nil {
		t.Fatal(err)
	}
	if starts != 2 || live != 0 || maximum != 1 || dispatches != 1 || !strings.Contains(output.String(), "Image:") {
		t.Fatalf("starts=%d live=%d max=%d dispatches=%d output=%q", starts, live, maximum, dispatches, output.String())
	}
}

func TestOptionalFileHintCannotOverrideMagic(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("direct executable fixtures are Unix-specific")
	}
	t.Run("classifies otherwise binary input", func(t *testing.T) {
		tools, fileLog, chafaLog := t.TempDir(), filepath.Join(t.TempDir(), "file"), filepath.Join(t.TempDir(), "chafa")
		installPreviewToolOutput(t, tools, "file", fileLog, "image/png", 0)
		installPreviewTool(t, tools, "chafa", chafaLog, 0)
		path := filepath.Join(t.TempDir(), "unknown")
		if err := os.WriteFile(path, []byte{0, 1, 2}, 0o600); err != nil {
			t.Fatal(err)
		}
		var output bytes.Buffer
		options := testOptions(&output)
		options.Environment = []string{"PATH=" + tools}
		if err := Render(context.Background(), resolved(path), options); err != nil {
			t.Fatal(err)
		}
		assertToolLog(t, fileLog, []string{filepath.Join(tools, "file"), "--brief", "--mime-type", "--", path})
		assertToolLog(t, chafaLog, append([]string{filepath.Join(tools, "chafa")}, "--size", "80x40", "--", path))
		if strings.Contains(output.String(), "image/png") {
			t.Fatalf("file hint leaked to preview: %q", output.String())
		}
	})
	t.Run("known PNG skips contradictory hint", func(t *testing.T) {
		tools, fileLog, chafaLog := t.TempDir(), filepath.Join(t.TempDir(), "file"), filepath.Join(t.TempDir(), "chafa")
		installPreviewToolOutput(t, tools, "file", fileLog, "text/plain", 0)
		installPreviewTool(t, tools, "chafa", chafaLog, 0)
		path := writeFixtures(t, t.TempDir())["image.png"]
		var output bytes.Buffer
		options := testOptions(&output)
		options.Environment = []string{"PATH=" + tools}
		if err := Render(context.Background(), resolved(path), options); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(fileLog); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("file hint ran for authoritative magic: %v", err)
		}
		assertToolLog(t, chafaLog, append([]string{filepath.Join(tools, "chafa")}, "--size", "80x40", "--", path))
	})
}

func TestConverterCategoriesRemainNativeUntilTask15(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("direct executable fixtures are Unix-specific")
	}
	tools := t.TempDir()
	installPreviewTool(t, tools, "pdftotext", filepath.Join(t.TempDir(), "pdf"), 0)
	installPreviewTool(t, tools, "ffprobe", filepath.Join(t.TempDir(), "media"), 0)
	fixtures := writeFixtures(t, t.TempDir())
	for _, name := range []string{"document.pdf", "video.mp4"} {
		t.Run(name, func(t *testing.T) {
			var output bytes.Buffer
			options := testOptions(&output)
			options.Environment = []string{"PATH=" + tools}
			starts := 0
			options.Runner.Observe = func(event processpkg.ProcessEvent) {
				if event.Phase == "start" {
					starts++
				}
			}
			if err := Render(context.Background(), resolved(fixtures[name]), options); err != nil {
				t.Fatal(err)
			}
			if starts != 0 || !strings.Contains(output.String(), "modified") {
				t.Fatalf("starts=%d output=%q", starts, output.String())
			}
		})
	}
}

func TestZipAuthorityCannotBypassNativePreflight(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("direct executable fixtures are Unix-specific")
	}
	tools, log := t.TempDir(), filepath.Join(t.TempDir(), "unzip")
	installPreviewTool(t, tools, "unzip", log, 0)
	path := filepath.Join(t.TempDir(), "malformed.zip")
	if err := os.WriteFile(path, []byte("not a zip"), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	options := testOptions(&output)
	options.Environment = []string{"PATH=" + tools}
	if err := Render(context.Background(), resolved(path), options); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(log); !errors.Is(err, os.ErrNotExist) || !strings.Contains(output.String(), "zip file:") {
		t.Fatalf("unzip stat=%v output=%q", err, output.String())
	}
}

func TestFileHintedZipCannotBypassNativePreflight(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("direct executable fixtures are Unix-specific")
	}
	tools := t.TempDir()
	installPreviewToolOutput(t, tools, "file", filepath.Join(t.TempDir(), "file"), "application/zip", 0)
	unzipLog := filepath.Join(t.TempDir(), "unzip")
	installPreviewTool(t, tools, "unzip", unzipLog, 0)
	path := filepath.Join(t.TempDir(), "unknown")
	if err := os.WriteFile(path, []byte{0, 1, 2}, 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	options := testOptions(&output)
	options.Environment = []string{"PATH=" + tools}
	if err := Render(context.Background(), resolved(path), options); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(unzipLog); !errors.Is(err, os.ErrNotExist) || !strings.Contains(output.String(), "zip file:") {
		t.Fatalf("unzip stat=%v output=%q", err, output.String())
	}
}

func afterDoubleDash(path string) []string { return []string{"--", path} }

func installPreviewTool(t *testing.T, directory, name, log string, status int) {
	t.Helper()
	installPreviewToolOutput(t, directory, name, log, name+" output", status)
}

func installPreviewToolOutput(t *testing.T, directory, name, log, output string, status int) {
	t.Helper()
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$0\" \"$@\" > %s\nprintf '%%s\\n' %s\nexit %d\n", strconv.Quote(log), strconv.Quote(output), status)
	if err := os.WriteFile(filepath.Join(directory, name), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
}

func assertToolLog(t *testing.T, path string, want []string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("tool call=%q want %q", got, want)
	}
}

func testOptions(output *bytes.Buffer) Options {
	return Options{Columns: 80, Lines: 40, Environment: []string{"PATH="}, Runner: processpkg.Runner{},
		Limits: DefaultLimits, Stdout: output, Stderr: output}
}

func resolved(path string) protocol.ResolvedCandidate {
	return protocol.ResolvedCandidate{Kind: protocol.KindFile, Path: []byte(path)}
}
