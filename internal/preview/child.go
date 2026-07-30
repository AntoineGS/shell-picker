package preview

import (
	"io"
	"time"

	"github.com/AntoineGS/shell-picker/internal/process"
)

func externalProcessSpec(executable string, arguments, environment []string, stdout, stderr io.Writer) process.Spec {
	return process.Spec{Path: executable, Args: arguments, Env: environment, Stdout: stdout, Stderr: stderr,
		Containment: process.ContainmentInheritTree, WaitDelay: time.Second}
}

func waitConverter(wait <-chan error, ticks <-chan time.Time, temp *converterArtifact, session *renderSession) (error, error) {
	for {
		select {
		case err := <-wait:
			return err, nil
		case <-ticks:
			if size, err := temp.Size(); err == nil && size > maxCachedArtifactBytes {
				_ = temp.Cleanup()
				if session.tree != nil {
					_ = session.tree.KillTree()
				}
				<-wait
				_ = temp.Cleanup()
				return nil, session.terminal(ErrArtifactLimit)
			}
		}
	}
}
