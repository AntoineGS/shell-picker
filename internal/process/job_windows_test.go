//go:build windows

package process

import (
	"context"
	"errors"
	"testing"
)

func TestWindowsCancelTerminatesJob(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	child, err := (Runner{}).Start(ctx, helperSpec("block"))
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := child.Wait(); !errors.Is(err, context.Canceled) {
		t.Fatalf("wait=%v", err)
	}
}
