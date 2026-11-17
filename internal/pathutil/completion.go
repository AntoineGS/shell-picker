package pathutil

import (
	"path/filepath"
	"strings"
)

// CompletionPath separates the editable path from the filename search.
// Prefix retains the user's spelling; Directory is only used for enumeration.
type CompletionPath struct {
	Directory string
	Prefix    string
	Search    string
}

func ResolveCompletion(base, home, query string) CompletionPath {
	text := filepath.ToSlash(query)
	prefix, search := "", text
	if text == "~" || text == "." || text == ".." || strings.HasSuffix(text, "/..") || strings.HasSuffix(text, "/.") {
		prefix, search = text+"/", ""
	} else if index := strings.LastIndexByte(text, '/'); index >= 0 {
		prefix, search = text[:index+1], text[index+1:]
	}
	directory := filepath.FromSlash(prefix)
	if strings.HasPrefix(prefix, "~/") {
		directory = filepath.Join(home, filepath.FromSlash(prefix[2:]))
	} else if !filepath.IsAbs(directory) {
		directory = filepath.Join(base, directory)
	}
	return CompletionPath{Directory: filepath.Clean(directory), Prefix: prefix, Search: search}
}

// FormatCompletion uses the typed directory prefix when possible, including
// redundant ../ segments. Non-local results (e.g. zoxide) retain the same
// relative, absolute, or home-relative style.
func FormatCompletion(base, home, query, target string) string {
	resolved := ResolveCompletion(base, home, query)
	var text string
	if filepath.Dir(target) == resolved.Directory {
		text = resolved.Prefix + filepath.Base(target)
	} else if query == "~" || strings.HasPrefix(query, "~/") {
		relative, err := filepath.Rel(home, target)
		if err != nil {
			return target
		}
		text = "~/" + filepath.ToSlash(relative)
	} else if filepath.IsAbs(query) {
		text = target
	} else {
		relative, err := filepath.Rel(base, target)
		if err != nil {
			return target
		}
		text = relative
	}
	homeStyle := query == "~" || strings.HasPrefix(query, "~/")
	if strings.HasPrefix(text, "-") || !homeStyle && (text == "~" || strings.HasPrefix(text, "~/")) {
		text = "./" + text
	}
	return text
}
