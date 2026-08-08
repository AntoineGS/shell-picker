package fzfsidecar

import (
	"reflect"
	"runtime"
	"testing"
)

func TestEnabledRequiresExactValue(t *testing.T) {
	valid := []string{ActivationVariable + "=1"}
	if !Enabled(valid) {
		t.Fatal("Enabled() = false for the exact activation value")
	}

	for _, value := range []string{"", "0", "true", "01", " 1", "1 ", "1=extra"} {
		t.Run("value="+value, func(t *testing.T) {
			if Enabled([]string{ActivationVariable + "=" + value}) {
				t.Fatalf("Enabled() = true for value %q", value)
			}
		})
	}
	if Enabled([]string{"OTHER=value"}) {
		t.Fatal("Enabled() = true when activation variable is missing")
	}
}

func TestEnabledHandlesDuplicateActivationEntries(t *testing.T) {
	if runtime.GOOS == "windows" {
		if !Enabled([]string{ActivationVariable + "=0", "shell_picker_experimental_fzf_sidecar=1"}) {
			t.Fatal("Enabled() = false when the last Windows occurrence is 1")
		}
		if Enabled([]string{ActivationVariable + "=1", "shell_picker_experimental_fzf_sidecar=0"}) {
			t.Fatal("Enabled() = true when the last Windows occurrence is 0")
		}
		return
	}

	if Enabled([]string{ActivationVariable + "=0", ActivationVariable + "=1"}) {
		t.Fatal("Enabled() = true for conflicting duplicate entries")
	}
}

func TestEnabledUsesPlatformSpecificNameMatching(t *testing.T) {
	caseVariant := "shell_picker_experimental_fzf_sidecar=1"
	if got, want := Enabled([]string{caseVariant}), runtime.GOOS == "windows"; got != want {
		t.Fatalf("Enabled() = %v for case-variant name, want %v", got, want)
	}
	if runtime.GOOS == "windows" && !Enabled([]string{"=C:=C:\\work", ActivationVariable + "=1"}) {
		t.Fatal("Enabled() = false in the presence of a valid Windows drive entry")
	}
	if runtime.GOOS == "windows" && !Enabled([]string{"=::=::\\", ActivationVariable + "=1"}) {
		t.Fatal("Enabled() = false in the presence of Nushell's Windows pseudo-drive entry")
	}
	if runtime.GOOS == "windows" {
		for _, malformed := range []string{"=::=::", "=::=C:\\", "=::=::\\extra"} {
			if Enabled([]string{malformed, ActivationVariable + "=1"}) {
				t.Fatalf("Enabled() = true in the presence of malformed pseudo-drive entry %q", malformed)
			}
		}
	}
}

func TestEnabledRejectsMalformedEntriesAndDoesNotMutateInput(t *testing.T) {
	environment := []string{"MALFORMED", ActivationVariable + "=1", "OTHER=value"}
	original := append([]string(nil), environment...)
	if Enabled(environment) {
		t.Fatal("Enabled() = true for a malformed environment entry")
	}
	if !reflect.DeepEqual(environment, original) {
		t.Fatalf("Enabled() mutated input: got %q, want %q", environment, original)
	}

	for _, malformed := range []string{"", "=1", ActivationVariable, ActivationVariable + "\x00=1", ActivationVariable + "=1\x00"} {
		if Enabled([]string{malformed}) {
			t.Fatalf("Enabled() = true for malformed entry %q", malformed)
		}
	}
}
