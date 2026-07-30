//go:build linux

package integration

import (
	"reflect"
	"testing"
)

func TestParseControlledFZFEnvironmentReturnsOnlyActualCredentials(t *testing.T) {
	raw := []byte("PATH=/bin\x00SHELL_PICKER_ADDR=http://127.0.0.1:321\x00KEEP=yes\x00SHELL_PICKER_TOKEN=actual-token\x00")
	got, err := parseControlledFZFEnvironment(raw)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"http://127.0.0.1:321", "actual-token"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("credential count/order mismatch: got %d values want %d", len(got), len(want))
	}
}

func TestParseControlledFZFEnvironmentRejectsMissingOrDuplicateCredentials(t *testing.T) {
	for _, raw := range [][]byte{
		[]byte("SHELL_PICKER_ADDR=http://127.0.0.1:1\x00"),
		[]byte("SHELL_PICKER_ADDR=a\x00SHELL_PICKER_ADDR=b\x00SHELL_PICKER_TOKEN=t\x00"),
	} {
		if values, err := parseControlledFZFEnvironment(raw); err == nil || values != nil {
			t.Fatalf("malformed controlled environment accepted with %d values", len(values))
		}
	}
}
