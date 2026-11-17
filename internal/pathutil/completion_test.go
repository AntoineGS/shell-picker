//go:build !windows

package pathutil

import "testing"

func TestCompletionPathResolvesFromLaunchDirectory(t *testing.T) {
	for _, test := range []struct{ query, directory, prefix, search string }{
		{"", "/work/project", "", ""},
		{"read", "/work/project", "", "read"},
		{"src/ma", "/work/project/src", "src/", "ma"},
		{"../../..", "/", "../../../", ""},
		{"../", "/work", "../", ""},
		{"~/", "/home/test", "~/", ""},
		{"~", "/home/test", "~/", ""},
		{"~/notes/to", "/home/test/notes", "~/notes/", "to"},
		{"/", "/", "/", ""},
		{"/usr/lo", "/usr", "/usr/", "lo"},
		{"./", "/work/project", "./", ""},
		{"some dir/a", "/work/project/some dir", "some dir/", "a"},
		{"a~b", "/work/project", "", "a~b"},
	} {
		t.Run(test.query, func(t *testing.T) {
			got := ResolveCompletion("/work/project", "/home/test", test.query)
			if got.Directory != test.directory || got.Prefix != test.prefix || got.Search != test.search {
				t.Fatalf("resolved=%+v; want directory=%q prefix=%q search=%q", got, test.directory, test.prefix, test.search)
			}
		})
	}
}

func TestCompletionOutputKeepsTypedPathStyle(t *testing.T) {
	for _, test := range []struct{ query, target, want string }{
		{"", "/work/project/file", "file"},
		{"../pro", "/work/project", "../project"},
		{"../../work/pro", "/work/project", "../../work/project"},
		{"./fi", "/work/project/file", "./file"},
		{"~", "/home/test/notes", "~/notes"},
		{"~/notes/to", "/home/test/notes/todo", "~/notes/todo"},
		{"/work/pro", "/work/project", "/work/project"},
		{"other", "/elsewhere/frequent", "../../elsewhere/frequent"},
		{"", "/work/project/-flag", "./-flag"},
		{"~", "/elsewhere/frequent", "~/../../elsewhere/frequent"},
		{"", "/work/project/~", "./~"},
	} {
		t.Run(test.query+test.target, func(t *testing.T) {
			if got := FormatCompletion("/work/project", "/home/test", test.query, test.target); got != test.want {
				t.Fatalf("got %q, want %q", got, test.want)
			}
		})
	}
}
