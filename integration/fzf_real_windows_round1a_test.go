//go:build windows

package integration

import "testing"

func (session *windowsTerminalSession) TraceEvents() []traceEvent {
	session.eventMu.Lock()
	defer session.eventMu.Unlock()
	return append([]traceEvent(nil), session.events...)
}

// AssertProcessTopology is intentionally compile-only in Round 1A. Native
// Windows process topology evidence remains assigned to the next pass.
func (session *windowsTerminalSession) AssertProcessTopology(t *testing.T) {
	t.Helper()
}
