//go:build linux

package integration

// exec.Cmd owns the stdout pipe and joins its copying worker in Wait, so
// waitDone also guarantees byte-exact result capture has finished.
type linuxResultWriter struct{ session *linuxTerminalSession }

func (writer linuxResultWriter) Write(data []byte) (int, error) {
	writer.session.outputMu.Lock()
	defer writer.session.outputMu.Unlock()
	return writer.session.result.Write(data)
}
