package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/AntoineGS/shell-picker/internal/fzfsidecar"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

// fzfSidecar is the app's narrow lifecycle seam for the optional fzf session.
type fzfSidecar interface {
	Address() string
	APIKey() string
	Start(context.Context)
	Stop()
	Wait() error
}

type fzfSidecarFactory func(protocol.Picker) (fzfSidecar, error)

type fzfSidecarLifecycle struct {
	session fzfSidecar
	created bool
	started bool
	stopped bool
	waited  bool
	waitErr error
}

func (lifecycle *fzfSidecarLifecycle) create(enabled bool, picker protocol.Picker, factory fzfSidecarFactory, observer fzfsidecar.Observer) error {
	if !enabled {
		return nil
	}
	if factory == nil {
		factory = func(picker protocol.Picker) (fzfSidecar, error) {
			if observer == nil {
				return fzfsidecar.New(picker)
			}
			return fzfsidecar.New(picker, fzfsidecar.WithObserver(observer))
		}
	}
	session, err := factory(picker)
	if err != nil {
		return fmt.Errorf("create fzf sidecar: %w", err)
	}
	if session == nil {
		return errors.New("create fzf sidecar: nil session")
	}
	lifecycle.session, lifecycle.created = session, true
	return nil
}

func (lifecycle *fzfSidecarLifecycle) credentials() (address, apiKey string) {
	if !lifecycle.created {
		return "", ""
	}
	return lifecycle.session.Address(), lifecycle.session.APIKey()
}

func (lifecycle *fzfSidecarLifecycle) start(ctx context.Context) {
	if !lifecycle.created || lifecycle.started {
		return
	}
	lifecycle.session.Start(ctx)
	lifecycle.started = true
}

func (lifecycle *fzfSidecarLifecycle) stopAndWait() error {
	if !lifecycle.created {
		return nil
	}
	if !lifecycle.stopped {
		lifecycle.session.Stop()
		lifecycle.stopped = true
	}
	if !lifecycle.waited {
		lifecycle.waitErr = lifecycle.session.Wait()
		lifecycle.waited = true
	}
	return lifecycle.waitErr
}
