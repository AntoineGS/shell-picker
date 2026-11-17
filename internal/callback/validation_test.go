package callback

import (
	"errors"
	"testing"
)

func TestSourceToggleRequiresCtrlF(t *testing.T) {
	for _, key := range []string{"ctrl-f", "enter", ""} {
		err := ValidateLocal(mustParse(t, "e:sf"), func(name string) string {
			if name == "FZF_KEY" {
				return key
			}
			return ""
		})
		if key == "ctrl-f" && err != nil {
			t.Errorf("ctrl-f rejected: %v", err)
		}
		if key != "ctrl-f" && !errors.Is(err, ErrKey) {
			t.Errorf("key %q err=%v, want ErrKey", key, err)
		}
	}
}

func TestMarkMutationControlsAreAuthorized(t *testing.T) {
	for _, key := range []string{"space", "ctrl-space", "btab", "shift-tab", "right-click", "shift-right-click"} {
		if err := ValidateLocal(mustParse(t, "e:mk"), func(name string) string {
			if name == "FZF_KEY" {
				return key
			}
			return ""
		}); err != nil {
			t.Errorf("mark key %q rejected: %v", key, err)
		}
	}
}
