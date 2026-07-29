package preview

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDetectCoversNativeCategories(t *testing.T) {
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
			path := fixtures[name]
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			var prefix []byte
			if !info.IsDir() {
				prefix, err = os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if len(prefix) > 64<<10 {
					prefix = prefix[:64<<10]
				}
			}
			got, err := Detect(prefix, info)
			if err != nil || got != want {
				t.Fatalf("Detect(%s)=(%q,%v), want %q", name, got, err, want)
			}
		})
	}
}

func TestDetectExtendedCategoriesAndMagicPrecedence(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name string
		data []byte
		want Category
	}{
		{name: "README", data: []byte("# title\n"), want: CategoryMarkdown},
		{name: "plain", data: []byte("plain text\n"), want: CategoryText},
		{name: "image.pdf", data: append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...), want: CategoryImage},
		{name: "document.png", data: []byte("%PDF-1.7\n"), want: CategoryPDF},
		{name: "binary.txt", data: []byte{0, 1, 2, 3}, want: CategoryBinary},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, tc.name)
			if err := os.WriteFile(path, tc.data, 0o600); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			got, err := Detect(tc.data, info)
			if err != nil || got != tc.want {
				t.Fatalf("Detect()=(%q,%v), want %q", got, err, tc.want)
			}
		})
	}
}

func TestParseCompletionInputCompatibility(t *testing.T) {
	dir := t.TempDir()
	actual := filepath.Join(dir, "actual")
	spaced := filepath.Join(dir, "with space")
	for _, path := range []string{actual, spaced} {
		if err := os.WriteFile(path, []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name    string
		input   []byte
		literal bool
		home    []byte
		want    string
		line    int
	}{
		{name: "second NBSP field", input: []byte("display\u00a0" + actual + "\u00a0index"), want: actual},
		{name: "literal NBSP", input: []byte(actual + "\u00a0suffix"), literal: true, want: actual + "\u00a0suffix"},
		{name: "literal line suffix", input: []byte(actual + ":12"), literal: true, want: actual + ":12"},
		{name: "home expansion", input: []byte("~/actual"), literal: true, home: []byte(dir), want: actual},
		{name: "home expansion trailing separator", input: []byte("~/actual"), literal: true, home: []byte(dir + string(os.PathSeparator)), want: actual},
		{name: "fzf-tab escaping", input: []byte(strings.ReplaceAll(spaced, " ", "\\ ") + " "), want: spaced},
		{name: "line and column", input: []byte(actual + ":23:7"), want: actual, line: 23},
		{name: "unreadable suffix stays path", input: []byte(filepath.Join(dir, "missing") + ":9"), want: filepath.Join(dir, "missing") + ":9"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseCompletionInput(tc.input, tc.literal, tc.home)
			if err != nil || string(got.Path) != tc.want || got.Line != tc.line {
				t.Fatalf("ParseCompletionInput()=%+v, %v; want path=%q line=%d", got, err, tc.want, tc.line)
			}
		})
	}
}

func TestParseCompletionInputPreservesEscapedColonAndInvalidBytes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("invalid-byte filesystem names are Unix-specific")
	}
	dir := []byte(t.TempDir())
	base := append(append(bytes.Clone(dir), filepath.Separator), []byte("foo")...)
	literalColon := append(bytes.Clone(base), []byte(":12")...)
	invalid := append(append(bytes.Clone(dir), filepath.Separator), []byte{'b', 'a', 'd', 0xff}...)
	for _, path := range [][]byte{base, literalColon, invalid} {
		if err := os.WriteFile(string(path), []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	escaped := append(append(bytes.Clone(base), '\\'), []byte(":12")...)
	got, err := ParseCompletionInput(escaped, false, nil)
	if err != nil || !bytes.Equal(got.Path, literalColon) || got.Line != 0 {
		t.Fatalf("escaped=%+v err=%v want path=%v", got, err, literalColon)
	}
	got, err = ParseCompletionInput(append(bytes.Clone(base), []byte(":12")...), false, nil)
	if err != nil || !bytes.Equal(got.Path, base) || got.Line != 12 {
		t.Fatalf("suffix=%+v err=%v want path=%v line=12", got, err, base)
	}
	got, err = ParseCompletionInput(append(bytes.Clone(invalid), []byte(":7")...), false, nil)
	if err != nil || !bytes.Equal(got.Path, invalid) || got.Line != 7 {
		t.Fatalf("invalid=%+v err=%v want path=%v line=7", got, err, invalid)
	}
}

func writeFixtures(t *testing.T, dir string) map[string]string {
	t.Helper()
	paths := make(map[string]string)
	write := func(name string, data []byte) {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		paths[name] = path
	}
	directory := filepath.Join(dir, "directory")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join("directory", "child.txt"), []byte("child"))
	paths["directory"] = directory
	write("readme.md", []byte("# heading\nbody\n"))
	write("plain.txt", []byte("plain\ttext\nsecond\n"))
	var imageBytes bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 2, 3))
	img.Set(0, 0, color.White)
	if err := png.Encode(&imageBytes, img); err != nil {
		t.Fatal(err)
	}
	write("image.png", imageBytes.Bytes())
	write("document.pdf", []byte("%PDF-1.7\n1 0 obj\n(Printable title)\nendobj\n"))
	write("video.mp4", append([]byte{0, 0, 0, 24}, []byte("ftypisomvideo")...))
	write("audio.mp3", []byte("ID3\x04\x00\x00audio"))
	var zipBytes bytes.Buffer
	zw := zip.NewWriter(&zipBytes)
	zf, err := zw.Create("inside.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = zf.Write([]byte("zip content"))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	write("sample.zip", zipBytes.Bytes())
	var gzipBytes bytes.Buffer
	gw := gzip.NewWriter(&gzipBytes)
	gw.Name = "inside.txt"
	_, _ = gw.Write([]byte("gzip content"))
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	write("sample.gz", gzipBytes.Bytes())
	write("sample.xz", []byte("\xfd7zXZ\x00metadata"))
	var tarBytes bytes.Buffer
	tw := tar.NewWriter(&tarBytes)
	if err := tw.WriteHeader(&tar.Header{Name: "inside.txt", Mode: 0o600, Size: 3}); err != nil {
		t.Fatal(err)
	}
	_, _ = tw.Write([]byte("tar"))
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	write("sample.tar", tarBytes.Bytes())
	write("sample.bz2", []byte("BZh91AY&SYmetadata"))
	write("binary.bin", []byte{0, 1, 2, 0xff, 0xfe})
	return paths
}
