package fzfsidecar_test

import (
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/fzfsidecar"
)

func TestTimerOptionIsUsableByPackageConsumers(t *testing.T) {
	_ = fzfsidecar.WithTimer(func(time.Duration) fzfsidecar.Timer { return nil })
}
