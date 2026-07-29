package preview

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

type Category string

type ParsedInput struct {
	Path []byte
	Line int
}

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
	if len(prefix) > 64<<10 {
		prefix = prefix[:64<<10]
	}
	extension := strings.ToLower(filepath.Ext(info.Name()))
	switch {
	case bytes.HasPrefix(prefix, []byte("PK\x03\x04")):
		return CategoryZip, nil
	case bytes.HasPrefix(prefix, []byte("\x1f\x8b")):
		return CategoryGzip, nil
	case bytes.HasPrefix(prefix, []byte("\xfd7zXZ\x00")):
		return CategoryXz, nil
	case bytes.HasPrefix(prefix, []byte("BZh")):
		return CategoryBzip, nil
	case isTar(prefix):
		return CategoryTar, nil
	case bytes.HasPrefix(prefix, []byte("%PDF-")):
		return CategoryPDF, nil
	case isImage(prefix, ""):
		return CategoryImage, nil
	case isVideo(prefix, ""):
		return CategoryVideo, nil
	case isAudio(prefix, ""):
		return CategoryAudio, nil
	case extension == ".zip":
		return CategoryZip, nil
	case extension == ".gz":
		return CategoryGzip, nil
	case extension == ".xz":
		return CategoryXz, nil
	case extension == ".bz2":
		return CategoryBzip, nil
	case extension == ".tar":
		return CategoryTar, nil
	case extension == ".pdf":
		return CategoryPDF, nil
	case isImage(nil, extension):
		return CategoryImage, nil
	case isVideo(nil, extension):
		return CategoryVideo, nil
	case isAudio(nil, extension):
		return CategoryAudio, nil
	case extension == ".md" || extension == ".markdown" || looksMarkdown(prefix):
		return CategoryMarkdown, nil
	case looksText(prefix):
		return CategoryText, nil
	default:
		return CategoryBinary, nil
	}
}

func ParseCompletionInput(input []byte, literal bool, home []byte) (ParsedInput, error) {
	path := bytes.Clone(input)
	var escaped []bool
	if !literal {
		if fields := bytes.Split(path, []byte("\u00a0")); len(fields) > 1 {
			path = bytes.Clone(fields[1])
		}
		if len(path) > 0 && path[len(path)-1] == ' ' {
			path = path[:len(path)-1]
		}
		path, escaped = unescapeCompletion(path)
	}
	if len(home) > 0 && bytes.HasPrefix(path, []byte("~/")) {
		remainder := bytes.Clone(path[2:])
		var remainderEscaped []bool
		if len(escaped) >= 2 {
			remainderEscaped = append([]bool(nil), escaped[2:]...)
		}
		path = bytes.Clone(home)
		if last := path[len(path)-1]; last != '/' && last != '\\' {
			path = append(path, filepath.Separator)
		}
		path = append(path, remainder...)
		if escaped != nil {
			escaped = append(make([]bool, len(path)-len(remainder)), remainderEscaped...)
		}
	}
	if literal {
		return ParsedInput{Path: path}, nil
	}
	path, line := splitReadableLine(path, escaped)
	return ParsedInput{Path: path, Line: line}, nil
}

func unescapeCompletion(path []byte) ([]byte, []bool) {
	result := make([]byte, 0, len(path))
	escaped := make([]bool, 0, len(path))
	for index := 0; index < len(path); index++ {
		wasEscaped := false
		if path[index] == '\\' && index+1 < len(path) {
			index++
			wasEscaped = true
		}
		result = append(result, path[index])
		escaped = append(escaped, wasEscaped)
	}
	return result, escaped
}

func splitReadableLine(path []byte, escaped []bool) ([]byte, int) {
	last := lastUnescapedColon(path, escaped, len(path))
	if last < 0 {
		return path, 0
	}
	value, err := strconv.Atoi(string(path[last+1:]))
	if err != nil || value < 1 {
		return path, 0
	}
	base, line := path[:last], value
	if previous := lastUnescapedColon(path, escaped, last); previous >= 0 {
		if parsed, parseErr := strconv.Atoi(string(base[previous+1:])); parseErr == nil && parsed > 0 && readable(base[:previous]) {
			return bytes.Clone(base[:previous]), parsed
		}
	}
	if readable(base) {
		return bytes.Clone(base), line
	}
	return path, 0
}

func lastUnescapedColon(path []byte, escaped []bool, before int) int {
	for index := before - 1; index >= 0; index-- {
		if path[index] == ':' && (len(escaped) <= index || !escaped[index]) {
			return index
		}
	}
	return -1
}

func readable(path []byte) bool {
	file, err := os.Open(string(path))
	if err != nil {
		return false
	}
	return file.Close() == nil
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

func looksMarkdown(prefix []byte) bool {
	return bytes.HasPrefix(prefix, []byte("# ")) || bytes.HasPrefix(prefix, []byte("## "))
}
