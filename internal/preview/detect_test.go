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
