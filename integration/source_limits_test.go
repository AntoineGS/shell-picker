package integration

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestGoSourceLineLimits(t *testing.T) {
	root := filepath.Clean("..")
	list := exec.Command("git", "ls-files", "-z", "--cached", "--", "cmd", "internal", "integration")
	list.Dir = root
	output, err := list.Output()
	if err != nil {
		t.Fatalf("list checked-in Go sources: %v", err)
	}
	files := strings.Split(strings.TrimSuffix(string(output), "\x00"), "\x00")
	sort.Strings(files)
	var offenders []string
	for _, relative := range files {
		if relative == "" || !strings.HasSuffix(relative, ".go") || strings.Contains("/"+relative+"/", "/vendor/") {
			continue
		}
		source, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
		if err != nil {
			t.Fatalf("read %s: %v", relative, err)
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), relative, source, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", relative, err)
		}
		if ast.IsGenerated(parsed) {
			continue
		}
		lines := bytes.Count(source, []byte("\n"))
		if len(source) > 0 && source[len(source)-1] != '\n' {
			lines++
		}
		limit := 350
		if strings.HasSuffix(relative, "_test.go") {
			limit = 500
		}
		if lines > limit {
			offenders = append(offenders, relative+": "+itoa(lines)+" lines (limit "+itoa(limit)+")")
		}
	}
	if len(offenders) > 0 {
		t.Fatalf("Go source line limits exceeded:\n%s", strings.Join(offenders, "\n"))
	}
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	var digits [20]byte
	index := len(digits)
	for value > 0 {
		index--
		digits[index] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[index:])
}
