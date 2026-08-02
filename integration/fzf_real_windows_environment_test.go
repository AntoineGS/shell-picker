//go:build windows

package integration

import (
	"slices"
	"testing"
	"unicode/utf16"
)

func TestWindowsEnvironmentBuildsCreateProcessBlock(t *testing.T) {
	got, err := windowsEnvironment([]string{"z=last", `Path=C:\tools`, "A=first"})
	if err != nil {
		t.Fatal(err)
	}
	want := utf16.Encode([]rune("A=first\x00Path=C:\\tools\x00z=last\x00\x00"))
	if !slices.Equal(got, want) {
		t.Fatalf("environment block=%v, want %v", got, want)
	}
}

func TestWindowsEnvironmentPreservesUnicode(t *testing.T) {
	got, err := windowsEnvironment([]string{"ZED=\u00e9", "A=\U0001f642\u4e16\u754c"})
	if err != nil {
		t.Fatal(err)
	}
	want := utf16.Encode([]rune("A=\U0001f642\u4e16\u754c\x00ZED=\u00e9\x00\x00"))
	if !slices.Equal(got, want) {
		t.Fatalf("Unicode environment block=%v, want %v", got, want)
	}
}

func TestWindowsEnvironmentEmptyBlockIsDoubleNUL(t *testing.T) {
	got, err := windowsEnvironment(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []uint16{0, 0}) {
		t.Fatalf("empty environment block=%v, want [0 0]", got)
	}
}

func TestWindowsEnvironmentRejectsEmbeddedNUL(t *testing.T) {
	got, err := windowsEnvironment([]string{"A=first", "Z=bad\x00value"})
	if err == nil {
		t.Fatal("embedded NUL was accepted")
	}
	if got != nil {
		t.Fatalf("invalid environment returned partial block=%v", got)
	}
}
