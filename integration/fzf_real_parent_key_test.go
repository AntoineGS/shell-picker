package integration

import (
	"testing"

	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestRealFZFCtrlHReturnsToParent(t *testing.T) {
	for _, mode := range []string{"insert", "normal"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newRealFZFFixture(t, requireRealFZF(t), "ctrl h parent")
			term := fixture.Start(t, protocol.PickerCP)
			defer term.Close()
			term.WaitBarrier(testContext(t), barrier{Event: "fzf.start", Count: 1})
			waitForTerminalText(t, term, "5/5")
			sendAndWait(t, term, []byte("alpha/"), barrier{Event: "callback.load", Generation: 2, Count: 1})
			waitForTerminalText(t, term, "2/2")
			if mode == "normal" {
				sendAndWait(t, term, keyEsc, barrier{Event: "callback.event", Operation: "es", Count: 1})
				waitForTerminalText(t, term, "[N]")
			}
			before := len(term.Output())
			sendAndWait(t, term, []byte{0x08}, barrier{Event: "callback.load", Generation: 3, Count: 1})
			waitForTerminalTextAfter(t, term, before, "5/5")
		})
	}
}
