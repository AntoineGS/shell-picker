package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/fzf"
	"github.com/AntoineGS/shell-picker/internal/fzfsidecar"
	"github.com/AntoineGS/shell-picker/internal/protocol"
	"github.com/AntoineGS/shell-picker/internal/sessionipc"
)

func TestRunPickerConcreteSidecarStopsBlockedRequestsOnFZFFailure(t *testing.T) {
	launchErr := errors.New("fzf failed while sidecar was polling")
	for _, test := range []struct {
		name   string
		mode   concreteSidecarMode
		method string
	}{
		{name: "GET", mode: concreteSidecarBlockedGET, method: "GET"},
		{name: "POST", mode: concreteSidecarBlockedPOST, method: "POST"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPickerFixture(t, protocol.PickerCP)
			fixture.dependencies.Environment = append(fixture.dependencies.Environment, fzfsidecar.ActivationVariable+"=1")
			factory, harness := newConcreteSidecarFactory(t, test.mode)
			fixture.dependencies.newFZFSidecar = factory

			var callback *sessionipc.Client
			fixture.dependencies.launchFZF = func(_ context.Context, config fzf.Config) (fzf.Result, error) {
				if !contains(config.Options, "--listen="+harness.session.Address()) {
					t.Fatalf("fzf options=%q, missing sidecar address", config.Options)
				}
				if config.ListenAPIKey != harness.session.APIKey() {
					t.Fatalf("fzf API key=%q, want session key", config.ListenAPIKey)
				}
				callback = callbackClient(t, config)
				harness.callback = callback
				waitForConcreteRequest(t, harness.started, test.method, harness)
				return fzf.Result{}, launchErr
			}

			_, err := RunPicker(context.Background(), fixture.options, fixture.dependencies)
			if !errors.Is(err, launchErr) {
				t.Fatalf("RunPicker error=%v, want %v", err, launchErr)
			}
			waitForConcreteRequest(t, harness.cancelled, test.method, harness)
			assertConcreteSidecarCleanup(t, harness, callback, true)
		})
	}
}

func TestRunPickerConcreteSidecarPropagatesParentCancellationDuringReadiness(t *testing.T) {
	fixture := newPickerFixture(t, protocol.PickerCP)
	fixture.dependencies.Environment = append(fixture.dependencies.Environment, fzfsidecar.ActivationVariable+"=1")
	factory, harness := newConcreteSidecarFactory(t, concreteSidecarBlockedReadiness)
	fixture.dependencies.newFZFSidecar = factory

	parent, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	cause := errors.New("parent cancelled picker")
	var callback *sessionipc.Client
	fixture.dependencies.launchFZF = func(_ context.Context, config fzf.Config) (fzf.Result, error) {
		callback = callbackClient(t, config)
		harness.callback = callback
		waitForConcreteRequest(t, harness.started, "GET", harness)
		cancel(cause)
		return fzf.Result{Aborted: true, ExitCode: 130}, nil
	}

	_, err := RunPicker(parent, fixture.options, fixture.dependencies)
	if !errors.Is(err, cause) {
		t.Fatalf("RunPicker error=%v, want parent cause %v", err, cause)
	}
	waitForConcreteRequest(t, harness.cancelled, "GET", harness)
	assertConcreteSidecarCleanup(t, harness, callback, false)
}

func TestRunPickerConcreteSidecarJoinsActivePostForAcceptedAndAborted(t *testing.T) {
	for _, test := range []struct {
		name   string
		result func(*testing.T, fzf.Config, pickerFixture) fzf.Result
		status protocol.Status
	}{
		{
			name: "accepted",
			result: func(t *testing.T, config fzf.Config, fixture pickerFixture) fzf.Result {
				return fzf.Result{Key: "enter", Records: [][]byte{recordForPath(t, config.Input, fixture.file)}}
			},
			status: protocol.StatusAccepted,
		},
		{
			name: "aborted",
			result: func(*testing.T, fzf.Config, pickerFixture) fzf.Result {
				return fzf.Result{Aborted: true, ExitCode: 130}
			},
			status: protocol.StatusAborted,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPickerFixture(t, protocol.PickerCP)
			fixture.dependencies.Environment = append(fixture.dependencies.Environment, fzfsidecar.ActivationVariable+"=1")
			factory, harness := newConcreteSidecarFactory(t, concreteSidecarBlockedPOST)
			fixture.dependencies.newFZFSidecar = factory
			var callback *sessionipc.Client
			fixture.dependencies.launchFZF = func(_ context.Context, config fzf.Config) (fzf.Result, error) {
				callback = callbackClient(t, config)
				harness.callback = callback
				waitForConcreteRequest(t, harness.active, "POST", harness)
				return test.result(t, config, fixture), nil
			}

			outcome, err := RunPicker(context.Background(), fixture.options, fixture.dependencies)
			if err != nil || outcome.Status != test.status {
				t.Fatalf("RunPicker() outcome=%+v err=%v, want status %q", outcome, err, test.status)
			}
			waitForConcreteRequest(t, harness.cancelled, "POST", harness)
			assertConcreteSidecarCleanup(t, harness, callback, true)
		})
	}
}

func TestRunPickerConcreteSidecarDoesNotForwardUnknownStateOrAPIKey(t *testing.T) {
	const stateCanary = "task5-unknown-state-canary"
	rawKey := []byte("0123456789abcdef0123456789abcdef")
	apiKey := base64.RawURLEncoding.EncodeToString(rawKey)
	fixture := newPickerFixture(t, protocol.PickerCP)
	fixture.dependencies.Environment = append(fixture.dependencies.Environment, fzfsidecar.ActivationVariable+"=1")
	tracePath := fixture.options.TracePath
	if tracePath == "" {
		tracePath = fmt.Sprintf("%s.trace", fixture.options.CWD)
		fixture.options.TracePath = tracePath
	}
	var ttyOut, ttyErr bytes.Buffer
	fixture.dependencies.TTYOut = &ttyOut
	fixture.dependencies.TTYErr = &ttyErr
	factory, harness := newConcreteSidecarFactory(t, concreteSidecarReady)
	harness.key = rawKey
	harness.stateCanary = stateCanary
	harness.observer = &concreteSidecarObserver{}
	fixture.dependencies.newFZFSidecar = factory
	var captured fzf.Config
	var callbackOutput string
	fixture.dependencies.launchFZF = func(_ context.Context, config fzf.Config) (fzf.Result, error) {
		captured = config
		harness.callback = callbackClient(t, config)
		waitForConcreteRequest(t, harness.active, "POST", harness)
		response, err := harness.callback.Display(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		callbackOutput = fmt.Sprintf("%+v", response)
		return fzf.Result{Key: "enter", Records: [][]byte{recordForPath(t, config.Input, fixture.file)}}, nil
	}
	if _, err := RunPicker(context.Background(), fixture.options, fixture.dependencies); err != nil {
		t.Fatal(err)
	}
	observerEvents := harness.observer.Events()
	if len(observerEvents) < 3 {
		t.Fatalf("sidecar observer events=%+v, want GET, POST, and stop diagnostics", observerEvents)
	}
	if observerEvents[0].Kind != fzfsidecar.ObserverGetSuccess || observerEvents[1].Kind != fzfsidecar.ObserverPostSuccess {
		t.Fatalf("sidecar observer operation categories=%+v, want GET/POST success", observerEvents)
	}
	if observerEvents[len(observerEvents)-1].Kind != fzfsidecar.ObserverStop {
		t.Fatalf("sidecar observer final event=%+v, want stop", observerEvents[len(observerEvents)-1])
	}
	if captured.ListenAPIKey != apiKey {
		t.Fatalf("captured API key=%q, want deterministic key", captured.ListenAPIKey)
	}
	posted := <-harness.posted
	trace, err := os.ReadFile(tracePath)
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		"fzf options":       strings.Join(captured.Options, "\x00"),
		"fzf environment":   strings.Join(captured.Environment, "\x00"),
		"post action":       posted,
		"trace":             string(trace),
		"terminal stdout":   ttyOut.String(),
		"terminal stderr":   ttyErr.String(),
		"callback response": callbackOutput,
	} {
		if strings.Contains(data, apiKey) || strings.Contains(data, stateCanary) {
			t.Fatalf("secret/canary leaked through %s: %q", name, data)
		}
	}
}
