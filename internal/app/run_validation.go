package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/protocol"
	"github.com/AntoineGS/shell-picker/internal/session"
)

func traceDiscardOutcome(err error) string {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded), errors.Is(err, session.ErrClosed):
		return "cancelled"
	case errors.Is(err, session.ErrStaleGeneration):
		return "stale"
	case errors.Is(err, session.ErrSuperseded):
		return "superseded"
	default:
		return "error"
	}
}

func validatePickerOptions(ctx context.Context, options PickerOptions) error {
	if ctx == nil {
		return errors.New("run picker: nil context")
	}
	if options.Picker != protocol.PickerCD && options.Picker != protocol.PickerCP && options.Picker != protocol.PickerSearch {
		return fmt.Errorf("run picker: invalid picker %q", options.Picker)
	}
	if options.Output != protocol.OutputNUL && options.Output != protocol.OutputNUON {
		return fmt.Errorf("run picker: invalid output %q", options.Output)
	}
	if options.ZoxidePolicy != candidate.ZoxideCached && options.ZoxidePolicy != candidate.ZoxideFresh {
		return errors.New("run picker: invalid zoxide policy")
	}
	if options.ZoxideTimeout < 0 {
		return errors.New("run picker: negative zoxide timeout")
	}
	if !filepath.IsAbs(string(options.ExecutablePath)) {
		return errors.New("run picker: executable path must be absolute")
	}
	for name, path := range map[string][]byte{"cwd": options.CWD, "home": options.Home} {
		if !filepath.IsAbs(string(path)) {
			return fmt.Errorf("run picker: %s must be absolute", name)
		}
		info, err := os.Stat(string(path))
		if err != nil || !info.IsDir() {
			return fmt.Errorf("run picker: %s must be an existing directory", name)
		}
	}
	return context.Cause(ctx)
}
