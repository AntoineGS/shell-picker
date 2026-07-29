package preview

import (
	"bytes"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

type Category string

const (
	CategoryDirectory Category = "directory"
	CategoryMarkdown  Category = "markdown"
	CategoryText      Category = "text"
	CategoryImage     Category = "image"
	CategoryPDF       Category = "pdf"
	CategoryVideo     Category = "video"
	CategoryAudio     Category = "audio"
	CategoryZip       Category = "zip"
	CategoryGzip      Category = "gzip"
	CategoryXz        Category = "xz"
	CategoryTar       Category = "tar"
	CategoryBzip      Category = "bzip"
	CategoryBinary    Category = "binary"
)

func Detect(prefix []byte, info fs.FileInfo) (Category, error) {
	if info == nil {
		return "", errors.New("preview: nil file information")
	}
	if info.IsDir() {
		return CategoryDirectory, nil
	}
	extension := strings.ToLower(filepath.Ext(info.Name()))
	switch {
	case bytes.HasPrefix(prefix, []byte("PK\x03\x04")) || extension == ".zip":
		return CategoryZip, nil
	case bytes.HasPrefix(prefix, []byte("\x1f\x8b")) || extension == ".gz":
		return CategoryGzip, nil
	case bytes.HasPrefix(prefix, []byte("\xfd7zXZ\x00")) || extension == ".xz":
		return CategoryXz, nil
	case bytes.HasPrefix(prefix, []byte("BZh")) || extension == ".bz2":
		return CategoryBzip, nil
	case isTar(prefix) || extension == ".tar":
		return CategoryTar, nil
	case bytes.HasPrefix(prefix, []byte("%PDF-")) || extension == ".pdf":
		return CategoryPDF, nil
	case isImage(prefix, extension):
		return CategoryImage, nil
	case isVideo(prefix, extension):
		return CategoryVideo, nil
	case isAudio(prefix, extension):
		return CategoryAudio, nil
	case extension == ".md" || extension == ".markdown":
		return CategoryMarkdown, nil
	case looksText(prefix):
		return CategoryText, nil
	default:
		return CategoryBinary, nil
	}
}

func isTar(prefix []byte) bool {
	return len(prefix) >= 262 && bytes.Equal(prefix[257:262], []byte("ustar"))
}

func isImage(prefix []byte, extension string) bool {
	return bytes.HasPrefix(prefix, []byte("\x89PNG\r\n\x1a\n")) || bytes.HasPrefix(prefix, []byte("\xff\xd8\xff")) ||
		bytes.HasPrefix(prefix, []byte("GIF87a")) || bytes.HasPrefix(prefix, []byte("GIF89a")) ||
		extension == ".png" || extension == ".jpg" || extension == ".jpeg" || extension == ".gif"
}

func isVideo(prefix []byte, extension string) bool {
	return (len(prefix) >= 8 && bytes.Equal(prefix[4:8], []byte("ftyp"))) ||
		extension == ".mp4" || extension == ".mkv" || extension == ".webm" || extension == ".avi"
}

func isAudio(prefix []byte, extension string) bool {
	return bytes.HasPrefix(prefix, []byte("ID3")) || bytes.HasPrefix(prefix, []byte("OggS")) ||
		extension == ".mp3" || extension == ".flac" || extension == ".wav" || extension == ".ogg"
}

func looksText(prefix []byte) bool {
	if bytes.IndexByte(prefix, 0) >= 0 || !utf8.Valid(prefix) {
		return false
	}
	for _, value := range prefix {
		if value < 0x09 || (value > 0x0d && value < 0x20) {
			return false
		}
	}
	return true
}
