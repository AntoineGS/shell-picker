//go:build windows

package pathutil

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
)

func TestWindowsParentModel(t *testing.T) {
	cases := []struct {
		in       Location
		wantKind Kind
		want     string
	}{
		{Filesystem([]byte(`C:\`)), KindDrives, ""},
		{Filesystem([]byte(`\\server\share\`)), KindDrives, ""},
		{Filesystem([]byte(`C:\work\child`)), KindFilesystem, `C:\work`},
		{Drives(), KindDrives, ""},
	}
	for _, tc := range cases {
		got := parentWindows(tc.in)
		if got.Kind != tc.wantKind || string(got.Path) != tc.want {
			t.Fatalf("in=%q got=%+v", tc.in.Path, got)
		}
	}
	if got := relativeWindows([]byte(`C:\work`), []byte(`D:\data\x`)); string(got) != `D:\data\x` {
		t.Fatal(string(got))
	}
	if got := PromptDisplay(Filesystem([]byte(`C:\`))); got != `C:\` {
		t.Fatalf("root prompt=%q", got)
	}
	if got := PromptDisplay(Drives()); got != `Drives\` {
		t.Fatalf("drives prompt=%q", got)
	}
}

func TestWindowsRelativeAndValidation(t *testing.T) {
	if got := string(relativeWindows([]byte(`C:\work`), []byte(`c:\work\-dash`))); got != `-dash` {
		t.Fatalf("relative=%q", got)
	}
	if got := PromptDisplay(Filesystem([]byte(`C:\work\\`))); got != `C:\work\` {
		t.Fatalf("prompt=%q", got)
	}
	for _, query := range [][]byte{nil, []byte(`C:\absolute`), []byte(`C:relative`), []byte(`\rooted`), []byte(`../escape`), []byte(`one\..\escape`)} {
		if err := ValidateAddQuery(Filesystem([]byte(`C:\work`)), query); !errors.Is(err, ErrInvalidAdd) {
			t.Fatalf("query %q: err=%v", query, err)
		}
	}
}

func TestCompactHomeWindows(t *testing.T) {
	tests := []struct {
		path, home, want string
	}{
		{`C:\Users\Test`, `c:\users\test`, `~`},
		{`C:\Users\Test\projects\app`, `C:\Users\Test`, `~\projects\app`},
		{`C:\Users\Test-old\app`, `C:\Users\Test`, `C:\Users\Test-old\app`},
		{`D:\app`, `C:\Users\Test`, `D:\app`},
	}
	for _, test := range tests {
		if got := string(CompactHome([]byte(test.path), []byte(test.home))); got != test.want {
			t.Errorf("CompactHome(%q, %q)=%q want=%q", test.path, test.home, got, test.want)
		}
	}
}

func TestPromptDisplayHomeWindows(t *testing.T) {
	home := Filesystem([]byte(`C:\Users\Test`))
	if got := PromptDisplayHome(home, home); got != `~\` {
		t.Fatalf("exact home display=%q", got)
	}
	got := PromptDisplayHome(Filesystem([]byte(`C:\Users\Test\app`)), home)
	if got != `~\app\` {
		t.Fatalf("PromptDisplayHome=%q", got)
	}
}

func TestWindowsUNCNavigationPure(t *testing.T) {
	t.Run("parent child", func(t *testing.T) {
		got := parentWindows(Filesystem([]byte(`\\server\share\team\project`)))
		if got.Kind != KindFilesystem || string(got.Path) != `\\server\share\team` {
			t.Fatalf("parent=%+v", got)
		}
	})

	t.Run("relative within share", func(t *testing.T) {
		got := relativeWindows([]byte(`\\server\share\team`), []byte(`\\SERVER\SHARE\team\project\child`))
		if string(got) != `project\child` {
			t.Fatalf("relative=%q", got)
		}
	})

	for _, tc := range []struct {
		name   string
		base   []byte
		target []byte
	}{
		{"cross share", []byte(`\\server\share-a\team`), []byte(`\\server\share-b\project`)},
		{"cross server", []byte(`\\server-a\share\team`), []byte(`\\server-b\share\project`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := relativeWindows(tc.base, tc.target)
			if !reflect.DeepEqual(got, tc.target) {
				t.Fatalf("relative=%q want unchanged %q", got, tc.target)
			}
			got[0] = 'X'
			if tc.target[0] != '\\' {
				t.Fatal("cross-volume result aliases target bytes")
			}
		})
	}
}

func TestWindowsUNCMixedAndRepeatedSeparatorsPure(t *testing.T) {
	parent := parentWindows(Filesystem([]byte(`\\server/share\\team//project\\`)))
	if parent.Kind != KindFilesystem || string(parent.Path) != `\\server\share\team` {
		t.Fatalf("parent=%+v", parent)
	}

	relative := relativeWindows(
		[]byte(`\\server/share\\team\`),
		[]byte(`\\SERVER\SHARE/team\\project//child`),
	)
	if string(relative) != `project\child` {
		t.Fatalf("relative=%q", relative)
	}
}

func TestListDrivesAscending(t *testing.T) {
	drives, err := ListDrives()
	if err != nil {
		t.Fatal(err)
	}
	for index, drive := range drives {
		if drive.Kind != KindFilesystem || len(drive.Path) != 3 || drive.Path[1] != ':' || drive.Path[2] != '\\' {
			t.Fatalf("invalid drive: %+v", drive)
		}
		if index > 0 && drives[index-1].Path[0] >= drive.Path[0] {
			t.Fatalf("drives not ascending: %q", drives)
		}
	}
}

func TestAbsoluteAncestryWindowsDrive(t *testing.T) {
	got, err := absoluteAncestryWindows(`C:\team\project`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{`C:\`, `C:\team`, `C:\team\project`}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%q want=%q", got, want)
	}
}

func TestAbsoluteAncestryWindowsUNC(t *testing.T) {
	got, err := absoluteAncestryWindows(`\\server\share\team\project`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{`\\server\share\`, `\\server\share\team`, `\\server\share\team\project`}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%q want=%q", got, want)
	}
	for _, invalid := range []string{"", `relative\path`, `C:relative`, `\\server`} {
		if _, err := absoluteAncestryWindows(invalid); err == nil {
			t.Fatalf("accepted %q", invalid)
		}
	}
}

func TestCreateDirectoryTreeRejectsJunctionInBaseAncestry(t *testing.T) {
	base := nativeJunctionBaseFixture(t)
	if _, err := CreateDirectoryTree(Filesystem([]byte(base)), []byte(`child`)); !errors.Is(err, ErrUnsafeTraversal) {
		t.Fatalf("err=%v", err)
	}
	hidden := filepath.Dir(base) + `\..\target\base`
	if _, err := CreateDirectoryTree(Filesystem([]byte(hidden)), []byte(`child`)); !errors.Is(err, ErrUnsafeTraversal) {
		t.Fatalf("dot-dot-hidden junction err=%v", err)
	}
}

func TestCreateDirectoryTreeRejectsJunctionInQueryComponent(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "base")
	target := filepath.Join(root, "target")
	junction := filepath.Join(base, "link")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(target, "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(junction, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := createJunction(junction, target); err != nil {
		t.Fatalf("create junction: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Remove(junction); err != nil && !os.IsNotExist(err) {
			t.Errorf("remove junction: %v", err)
		}
	})

	if _, err := CreateDirectoryTree(Filesystem([]byte(base)), []byte(`link\child`)); !errors.Is(err, ErrUnsafeTraversal) {
		t.Fatalf("err=%v", err)
	}
}

func TestCreateDirectoryTreeWindowsRollbackAndExistingFile(t *testing.T) {
	root := t.TempDir()
	created, err := CreateDirectoryTree(Filesystem([]byte(root)), []byte(`one\two`))
	if err != nil {
		t.Fatal(err)
	}
	if err := created.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root, "one")); !os.IsNotExist(err) {
		t.Fatalf("rollback err=%v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "file"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateDirectoryTree(Filesystem([]byte(root)), []byte(`file\child`)); err == nil {
		t.Fatal("accepted file component")
	}
	existing := filepath.Join(root, "existing")
	if err := os.Mkdir(existing, 0o755); err != nil {
		t.Fatal(err)
	}
	tooLong := make([]byte, 256)
	for index := range tooLong {
		tooLong[index] = 'x'
	}
	if _, err := CreateDirectoryTree(Filesystem([]byte(root)), append([]byte(`existing\temporary\`), tooLong...)); err == nil {
		t.Fatal("expected component creation failure")
	}
	if _, err := os.Stat(existing); err != nil {
		t.Fatalf("pre-existing parent removed: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(existing, "temporary")); !os.IsNotExist(err) {
		t.Fatalf("partial tree not rolled back: %v", err)
	}
}

func nativeJunctionBaseFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	target := filepath.Join(root, "target")
	junction := filepath.Join(root, "junction")
	base := filepath.Join(junction, "base")
	if err := os.MkdirAll(filepath.Join(target, "base"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(junction, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := createJunction(junction, target); err != nil {
		t.Fatalf("create junction: %v", err)
	}
	return base
}

func createJunction(path, target string) error {
	const (
		genericWrite             = 0x40000000
		openExisting             = 3
		fileFlagOpenReparsePoint = 0x00200000
		fileFlagBackupSemantics  = 0x02000000
		fsctlSetReparsePoint     = 0x000900a4
		ioReparseTagMountPoint   = 0xa0000003
	)
	handle, err := syscall.CreateFile(syscall.StringToUTF16Ptr(path), genericWrite, 0, nil, openExisting,
		fileFlagOpenReparsePoint|fileFlagBackupSemantics, 0)
	if err != nil {
		return err
	}
	defer syscall.CloseHandle(handle)

	substitute := syscall.StringToUTF16(`\??\` + target)
	printName := syscall.StringToUTF16(target)
	pathBytes := make([]byte, 2*(len(substitute)+len(printName)))
	for i, value := range substitute {
		binary.LittleEndian.PutUint16(pathBytes[i*2:], value)
	}
	printOffset := len(substitute) * 2
	for i, value := range printName {
		binary.LittleEndian.PutUint16(pathBytes[printOffset+i*2:], value)
	}
	reparseDataLength := 8 + len(pathBytes)
	buffer := make([]byte, 8+reparseDataLength)
	binary.LittleEndian.PutUint32(buffer[0:], ioReparseTagMountPoint)
	binary.LittleEndian.PutUint16(buffer[4:], uint16(reparseDataLength))
	binary.LittleEndian.PutUint16(buffer[8:], 0)
	binary.LittleEndian.PutUint16(buffer[10:], uint16((len(substitute)-1)*2))
	binary.LittleEndian.PutUint16(buffer[12:], uint16(printOffset))
	binary.LittleEndian.PutUint16(buffer[14:], uint16((len(printName)-1)*2))
	copy(buffer[16:], pathBytes)

	var returned uint32
	return syscall.DeviceIoControl(handle, fsctlSetReparsePoint, &buffer[0], uint32(len(buffer)), nil, 0, &returned, nil)
}
