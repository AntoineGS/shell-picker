//go:build !windows

package pathutil

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnixLocationModel(t *testing.T) {
	if got := Root(); got.Kind != KindFilesystem || !bytes.Equal(got.Path, []byte("/")) {
		t.Fatalf("Root() = %+v", got)
	}
	if got := Parent(Filesystem([]byte("/"))); got.Kind != KindFilesystem || string(got.Path) != "/" {
		t.Fatalf("Parent(root) = %+v", got)
	}
	if got := Parent(Filesystem([]byte("/work/child"))); string(got.Path) != "/work" {
		t.Fatalf("Parent(child) = %+v", got)
	}
	if got := Parent(Drives()); got.Kind != KindFilesystem || string(got.Path) != "/" {
		t.Fatalf("Parent(Drives()) = %+v", got)
	}
}

func TestRelativeAndAddValidation(t *testing.T) {
	if got := string(Relative([]byte("/work"), []byte("/work/-dash"))); got != "./-dash" {
		t.Fatal(got)
	}
	if got := string(Relative([]byte("/work"), []byte("/work/a\n"))); got != "a\n" {
		t.Fatalf("%q", got)
	}
	if got := PromptDisplay(Filesystem([]byte(`/work/a\b`))); got != `/work/a\\b/` {
		t.Fatalf("prompt=%q", got)
	}
	for _, query := range [][]byte{nil, []byte("/absolute"), []byte("../escape"), []byte("one/../escape")} {
		if err := ValidateAddQuery(Filesystem([]byte("/work")), query); !errors.Is(err, ErrInvalidAdd) {
			t.Fatalf("query %q: err=%v", query, err)
		}
	}
	if err := ValidateAddQuery(Drives(), []byte("projects/new")); !errors.Is(err, ErrInvalidAdd) {
		t.Fatalf("Drives query err=%v", err)
	}
	if err := ValidateAddQuery(Filesystem([]byte("/work")), []byte("projects/new")); err != nil {
		t.Fatal(err)
	}
}

func TestCompactHomeUnix(t *testing.T) {
	tests := []struct {
		name, path, home, want string
	}{
		{"exact", "/home/test", "/home/test", "~"},
		{"descendant", "/home/test/projects/app", "/home/test", "~/projects/app"},
		{"outside", "/srv/app", "/home/test", "/srv/app"},
		{"shared-prefix", "/home/test-old/app", "/home/test", "/home/test-old/app"},
		{"root", "/", "/home/test", "/"},
		{"invalid-home", "/home/test/app", "relative", "/home/test/app"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := string(CompactHome([]byte(test.path), []byte(test.home))); got != test.want {
				t.Fatalf("CompactHome(%q, %q)=%q want=%q", test.path, test.home, got, test.want)
			}
		})
	}
}

func TestPromptDisplayHomeUnix(t *testing.T) {
	location := Filesystem([]byte("/home/test/a\\b"))
	home := Filesystem([]byte("/home/test"))
	if got := PromptDisplayHome(location, home); got != `~/a\\b/` {
		t.Fatalf("PromptDisplayHome=%q", got)
	}
	if got := PromptDisplayHome(Drives(), home); got != "Drives/" {
		t.Fatalf("drives display=%q", got)
	}
}

func TestRelativePreservesArbitraryBytes(t *testing.T) {
	target := append([]byte("/work/"), 0xff, '\n', 'x')
	if got := Relative([]byte("/work"), target); !bytes.Equal(got, target[len("/work/"):]) {
		t.Fatalf("got=%q want=%q", got, target[len("/work/"):])
	}
}

func TestCreateDirectoryTreeRejectsSymlinkAndRollsBack(t *testing.T) {
	root := t.TempDir()
	if err := os.Symlink(filepath.Join(root, "outside"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateDirectoryTree(Filesystem([]byte(root)), []byte("link/child")); !errors.Is(err, ErrUnsafeTraversal) {
		t.Fatalf("err=%v", err)
	}
	created, err := CreateDirectoryTree(Filesystem([]byte(root)), []byte("one/two"))
	if err != nil {
		t.Fatal(err)
	}
	if string(created.Target.Path) != filepath.Join(root, "one", "two") || len(created.Created) != 2 {
		t.Fatalf("created=%+v", created)
	}
	if err := created.Rollback(); err != nil {
		t.Fatal(err)
	}
	if len(created.Created) != 0 {
		t.Fatalf("rollback retained paths: %q", created.Created)
	}
	if _, err := os.Lstat(filepath.Join(root, "one")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("rollback err=%v", err)
	}
}

func TestCreateDirectoryTreeRejectsSymlinkInBaseAncestry(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	if err := os.MkdirAll(filepath.Join(real, "base"), 0o755); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(root, "linked")
	if err := os.Symlink(real, linked); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateDirectoryTree(Filesystem([]byte(filepath.Join(linked, "base"))), []byte("child")); !errors.Is(err, ErrUnsafeTraversal) {
		t.Fatalf("err=%v", err)
	}
	hidden := linked + string(os.PathSeparator) + ".." + string(os.PathSeparator) + "real" + string(os.PathSeparator) + "base"
	if _, err := CreateDirectoryTree(Filesystem([]byte(hidden)), []byte("child")); !errors.Is(err, ErrUnsafeTraversal) {
		t.Fatalf("dot-dot-hidden symlink err=%v", err)
	}
}

func TestCreateDirectoryTreeErrorsAndPreservesExistingParents(t *testing.T) {
	root := t.TempDir()
	preexisting := filepath.Join(root, "existing")
	if err := os.Mkdir(preexisting, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(preexisting, "file"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := CreateDirectoryTree(Filesystem([]byte(root)), []byte("existing/file/child")); err == nil {
		t.Fatal("accepted a pre-existing file as a directory")
	}
	if _, err := os.Stat(preexisting); err != nil {
		t.Fatalf("pre-existing parent removed: %v", err)
	}

	tooLong := strings.Repeat("x", 256)
	if _, err := CreateDirectoryTree(Filesystem([]byte(root)), []byte("temporary/"+tooLong)); err == nil {
		t.Fatal("expected component creation failure")
	}
	if _, err := os.Lstat(filepath.Join(root, "temporary")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("partial tree not rolled back: %v", err)
	}
}

func TestRollbackIgnoresNonemptyAndNotExist(t *testing.T) {
	root := t.TempDir()
	created, err := CreateDirectoryTree(Filesystem([]byte(root)), []byte("one/two"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "one", "two", "keep"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	created.Created = append(created.Created, []byte(filepath.Join(root, "missing")))
	if err := created.Rollback(); err != nil {
		t.Fatalf("Rollback() = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "one", "two", "keep")); err != nil {
		t.Fatalf("nonempty created directory was altered: %v", err)
	}
}
