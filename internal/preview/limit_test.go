package preview

import (
	"bytes"
	"errors"
	"testing"
)

func TestCountingWriterReturnsOutputLimitWithoutExceedingBound(t *testing.T) {
	var destination bytes.Buffer
	writer := newCountingWriter(&destination, 4)
	n, err := writer.Write([]byte("abcdef"))
	if n != 4 || !errors.Is(err, ErrOutputLimit) || destination.String() != "abcd" {
		t.Fatalf("n=%d err=%v output=%q", n, err, destination.String())
	}
	if n, err = writer.Write([]byte("z")); n != 0 || !errors.Is(err, ErrOutputLimit) {
		t.Fatalf("second write n=%d err=%v", n, err)
	}
}

func TestNormalizedLimitsRestoreSecurityDefaults(t *testing.T) {
	got := normalizedLimits(Limits{})
	if got != DefaultLimits {
		t.Fatalf("got %+v want %+v", got, DefaultLimits)
	}
}
