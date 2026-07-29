//go:build windows

package preview

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

const testStageMarkerName = ".shell-picker-owner-v1"

func TestWindowsStaleCleanupRejectsAttackerStageLookalikes(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, string, string)
	}{
		{
			name: "exact artifact missing marker",
		},
		{
			name: "forged marker",
			setup: func(t *testing.T, directory, _ string) {
				writeStageFixture(t, directory, testStageMarkerName, []byte("forged"))
			},
		},
		{
			name: "mismatched marker nonce",
			setup: func(t *testing.T, directory, _ string) {
				writeStageFixture(t, directory, testStageMarkerName, testStageMarker(strings.Repeat("b", 32)))
			},
		},
		{
			name: "extra entry",
			setup: func(t *testing.T, directory, stageName string) {
				writeStageFixture(t, directory, testStageMarkerName, testStageMarker(stageName))
				writeStageFixture(t, directory, "extra", []byte("attacker"))
			},
		},
		{
			name: "hardlinked artifact",
			setup: func(t *testing.T, directory, stageName string) {
				writeStageFixture(t, directory, testStageMarkerName, testStageMarker(stageName))
				if err := os.Link(filepath.Join(directory, "artifact.jpg"), filepath.Join(t.TempDir(), "link")); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "hardlinked marker",
			setup: func(t *testing.T, directory, stageName string) {
				writeStageFixture(t, directory, testStageMarkerName, testStageMarker(stageName))
				if err := os.Link(filepath.Join(directory, testStageMarkerName), filepath.Join(t.TempDir(), "link")); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "reparse artifact",
			setup: func(t *testing.T, directory, stageName string) {
				writeStageFixture(t, directory, testStageMarkerName, testStageMarker(stageName))
				if err := os.Remove(filepath.Join(directory, "artifact.jpg")); err != nil {
					t.Fatal(err)
				}
				target := filepath.Join(t.TempDir(), "target")
				writeStageFixture(t, filepath.Dir(target), filepath.Base(target), []byte("attacker"))
				if err := os.Symlink(target, filepath.Join(directory, "artifact.jpg")); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "reparse marker",
			setup: func(t *testing.T, directory, stageName string) {
				if err := os.Remove(filepath.Join(directory, testStageMarkerName)); err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
				target := filepath.Join(t.TempDir(), "marker")
				writeStageFixture(t, filepath.Dir(target), filepath.Base(target), testStageMarker(stageName))
				if err := os.Symlink(target, filepath.Join(directory, testStageMarkerName)); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "permissive artifact",
			setup: func(t *testing.T, directory, stageName string) {
				writeStageFixture(t, directory, testStageMarkerName, testStageMarker(stageName))
				makeStageFixturePermissive(t, filepath.Join(directory, "artifact.jpg"))
			},
		},
		{
			name: "permissive marker",
			setup: func(t *testing.T, directory, stageName string) {
				writeStageFixture(t, directory, testStageMarkerName, testStageMarker(stageName))
				makeStageFixturePermissive(t, filepath.Join(directory, testStageMarkerName))
			},
		},
		{
			name: "permissive directory",
			setup: func(t *testing.T, directory, stageName string) {
				writeStageFixture(t, directory, testStageMarkerName, testStageMarker(stageName))
				makeStageFixturePermissive(t, directory)
			},
		},
	}

	for i, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			stageName := cacheTempPrefix + strings.Repeat(string("abcdef0123456789"[i]), 32)
			directory := filepath.Join(root, stageName)
			if err := os.Mkdir(directory, 0o700); err != nil {
				t.Fatal(err)
			}
			writeStageFixture(t, directory, "artifact.jpg", []byte("attacker"))
			if test.setup != nil {
				test.setup(t, directory, stageName)
			}
			before := stageFixtureEntries(t, directory)
			if _, err := NewCache(root, 512<<20); err != nil {
				t.Fatal(err)
			}
			if after := stageFixtureEntries(t, directory); !slices.Equal(after, before) {
				t.Fatalf("attacker entries changed: before=%v after=%v", before, after)
			}
			if data, err := os.ReadFile(filepath.Join(directory, "artifact.jpg")); err != nil || string(data) != "attacker" {
				t.Fatalf("attacker artifact changed: data=%q err=%v", data, err)
			}
		})
	}
}

func stageFixtureEntries(t *testing.T, directory string) []string {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(entries))
	for index, entry := range entries {
		names[index] = entry.Name()
	}
	return names
}

func TestWindowsStaleCleanupRemovesGenuineAbandonedStage(t *testing.T) {
	cache := mustNewCache(t, t.TempDir(), 512<<20)
	artifact, err := newConverterArtifact(cache, ".jpg")
	if err != nil {
		t.Fatal(err)
	}
	writer, err := artifact.OpenWritable()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("genuine")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := artifact.Validate(); err != nil {
		t.Fatal(err)
	}
	artifact.Abandon()
	if _, err := NewCache(cache.root, 512<<20); err != nil {
		t.Fatal(err)
	}
	assertNoCacheTemps(t, cache.root)
}

func writeStageFixture(t *testing.T, directory, name string, data []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(directory, name), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func testStageMarker(stageName string) []byte {
	return []byte("shell-picker-stage\x00\x01" + strings.TrimPrefix(stageName, cacheTempPrefix))
}

func makeStageFixturePermissive(t *testing.T, path string) {
	t.Helper()
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(pointer, windows.READ_CONTROL|windows.WRITE_DAC, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;;GA;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
}
