package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/fzf"
	"github.com/AntoineGS/shell-picker/internal/fzfsidecar"
	"github.com/AntoineGS/shell-picker/internal/protocol"
	"github.com/AntoineGS/shell-picker/internal/sessionipc"
)

func TestRunPickerEnabledSidecarConfiguresFZFListen(t *testing.T) {
	fixture := newPickerFixture(t, protocol.PickerCP)
	fixture.dependencies.Environment = append(fixture.dependencies.Environment, fzfsidecar.ActivationVariable+"=1")
	fixture.dependencies.launchFZF = func(_ context.Context, config fzf.Config) (fzf.Result, error) {
		var listen string
		for _, option := range config.Options {
			if strings.HasPrefix(option, "--listen=") {
				listen = strings.TrimPrefix(option, "--listen=")
			}
		}
		if listen == "" {
			t.Fatal("enabled sidecar did not configure an fzf listen address")
		}
		if config.ListenAPIKey == "" {
			t.Fatal("enabled sidecar did not configure an fzf API key")
		}
		return fzf.Result{Aborted: true, ExitCode: 130}, nil
	}

	outcome, err := RunPicker(context.Background(), fixture.options, fixture.dependencies)
	if err != nil {
		t.Fatalf("RunPicker() error = %v", err)
	}
	if outcome.Status != protocol.StatusAborted {
		t.Fatalf("RunPicker() outcome = %+v, want aborted", outcome)
	}
}

func TestRunPickerSidecarJoinsBeforeCallbackServerCloses(t *testing.T) {
	fixture := newPickerFixture(t, protocol.PickerCP)
	fixture.dependencies.Environment = append(fixture.dependencies.Environment, fzfsidecar.ActivationVariable+"=1")
	fake := newAppSidecar("127.0.0.1:4321", "sidecar-key")
	fixture.dependencies.newFZFSidecar = func(picker protocol.Picker) (fzfSidecar, error) {
		fake.events = append(fake.events, "create:"+string(picker))
		return fake, nil
	}
	var client *sessionipc.Client
	fake.wait = func() {
		fake.events = append(fake.events, "wait")
		if client == nil {
			fake.waitErr = errors.New("callback client was not created")
			return
		}
		_, fake.waitErr = client.Load(context.Background(), sessionipc.LoadRequest{Generation: 1})
	}
	fixture.dependencies.launchFZF = func(_ context.Context, config fzf.Config) (fzf.Result, error) {
		fake.events = append(fake.events, "launch")
		client = callbackClient(t, config)
		if config.ListenAPIKey != fake.key || !contains(config.Options, "--listen="+fake.address) {
			t.Fatalf("sidecar config=%+v", config)
		}
		return fzf.Result{Aborted: true, ExitCode: 130}, nil
	}

	outcome, err := RunPicker(context.Background(), fixture.options, fixture.dependencies)
	if err != nil || outcome.Status != protocol.StatusAborted {
		t.Fatalf("RunPicker() outcome=%+v err=%v", outcome, err)
	}
	if fake.waitErr != nil {
		t.Fatalf("callback server was closed before sidecar Wait: %v", fake.waitErr)
	}
	if got, want := fake.events, []string{"create:cp", "start", "launch", "stop", "wait"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("sidecar events=%q, want %q", got, want)
	}
	if _, err := client.Load(context.Background(), sessionipc.LoadRequest{Generation: 1}); err == nil {
		t.Fatal("callback server remained available after RunPicker returned")
	}
}

func TestRunPickerSidecarActivationIsCanonicalAndDisabledConfigIsLegacy(t *testing.T) {
	for _, test := range []struct {
		name       string
		activation string
		enabled    bool
	}{
		{name: "missing", enabled: false},
		{name: "zero", activation: "0", enabled: false},
		{name: "boolean", activation: "true", enabled: false},
		{name: "leading zero", activation: "01", enabled: false},
		{name: "canonical", activation: "1", enabled: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPickerFixture(t, protocol.PickerCD)
			if test.activation != "" {
				fixture.dependencies.Environment = append(fixture.dependencies.Environment,
					fzfsidecar.ActivationVariable+"="+test.activation)
			}
			created := false
			fake := newAppSidecar("127.0.0.1:4321", "sidecar-key")
			fixture.dependencies.newFZFSidecar = func(protocol.Picker) (fzfSidecar, error) {
				created = true
				return fake, nil
			}
			fixture.dependencies.launchFZF = func(_ context.Context, config fzf.Config) (fzf.Result, error) {
				for _, entry := range config.Environment {
					if strings.HasPrefix(entry, "SHELL_PICKER_") {
						t.Fatalf("activation/control environment reached fzf config: %q", entry)
					}
				}
				if test.enabled {
					if config.ListenAPIKey == "" || !contains(config.Options, "--listen="+fake.address) {
						t.Fatalf("enabled config=%+v", config)
					}
				} else if config.ListenAPIKey != "" || containsPrefix(config.Options, "--listen=") {
					t.Fatalf("disabled config=%+v", config)
				}
				return fzf.Result{Aborted: true, ExitCode: 130}, nil
			}

			if _, err := RunPicker(context.Background(), fixture.options, fixture.dependencies); err != nil {
				t.Fatalf("RunPicker() error = %v", err)
			}
			if created != test.enabled {
				t.Fatalf("sidecar created=%t, want %t", created, test.enabled)
			}
		})
	}
}

func TestRunPickerSidecarCreationFailureIsHardAndDoesNotLaunchFZF(t *testing.T) {
	fixture := newPickerFixture(t, protocol.PickerCP)
	fixture.dependencies.Environment = append(fixture.dependencies.Environment, fzfsidecar.ActivationVariable+"=1")
	creationErr := errors.New("sidecar construction failed")
	launched := false
	fixture.dependencies.newFZFSidecar = func(protocol.Picker) (fzfSidecar, error) {
		return nil, creationErr
	}
	fixture.dependencies.launchFZF = func(context.Context, fzf.Config) (fzf.Result, error) {
		launched = true
		return fzf.Result{}, nil
	}

	if _, err := RunPicker(context.Background(), fixture.options, fixture.dependencies); !errors.Is(err, creationErr) {
		t.Fatalf("RunPicker() error = %v, want %v", err, creationErr)
	}
	if launched {
		t.Fatal("fzf launched after sidecar construction failed")
	}
}

func TestRunPickerStopsCreatedSidecarWhenFZFOptionsValidationFails(t *testing.T) {
	fixture := newPickerFixture(t, protocol.PickerCP)
	fixture.dependencies.Environment = append(fixture.dependencies.Environment, fzfsidecar.ActivationVariable+"=1")
	fake := newAppSidecar("not-a-listen-address", "sidecar-key")
	var client *sessionipc.Client
	fixture.dependencies.listenIPC = func(ctx context.Context, token sessionipc.Token, backend sessionipc.Backend) (*sessionipc.Server, error) {
		server, err := sessionipc.Listen(ctx, token, backend)
		if err != nil {
			return nil, err
		}
		client, err = sessionipc.NewClientFromEnv(func(name string) string {
			switch name {
			case "SHELL_PICKER_ADDR":
				return server.Address()
			case "SHELL_PICKER_TOKEN":
				return token.String()
			default:
				return ""
			}
		})
		if err != nil {
			_ = server.Close(context.Background())
			return nil, err
		}
		return server, nil
	}
	fake.wait = func() {
		fake.events = append(fake.events, "wait")
		if client == nil {
			fake.waitErr = errors.New("callback client was not initialized")
			return
		}
		_, fake.waitErr = client.Load(context.Background(), sessionipc.LoadRequest{Generation: 1})
	}
	factoryCalled := false
	fixture.dependencies.newFZFSidecar = func(protocol.Picker) (fzfSidecar, error) {
		factoryCalled = true
		return fake, nil
	}
	fixture.dependencies.launchFZF = func(context.Context, fzf.Config) (fzf.Result, error) {
		t.Fatal("fzf launched after options validation failed")
		return fzf.Result{}, nil
	}

	if _, err := RunPicker(context.Background(), fixture.options, fixture.dependencies); err == nil {
		t.Fatal("RunPicker() succeeded with an invalid sidecar listen address")
	}
	if !factoryCalled {
		t.Fatal("sidecar factory was not called")
	}
	if fake.stopCalls != 1 || fake.waitCalls != 1 {
		t.Fatalf("sidecar Stop/Wait calls=(%d,%d), want (1,1); events=%q", fake.stopCalls, fake.waitCalls, fake.events)
	}
	if got, want := fake.events, []string{"stop", "wait"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("sidecar lifecycle events=%q, want %q", got, want)
	}
	if fake.waitErr != nil {
		t.Fatalf("sidecar Wait observed closed actor/server: %v", fake.waitErr)
	}
	if _, err := client.Load(context.Background(), sessionipc.LoadRequest{Generation: 1}); err == nil {
		t.Fatal("callback server remained available after RunPicker returned")
	}
}

func TestRunPickerEnabledSidecarPreservesAcceptedAndAbortedOutcomes(t *testing.T) {
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
			fake := newAppSidecar("127.0.0.1:4321", "sidecar-key")
			fixture.dependencies.newFZFSidecar = func(protocol.Picker) (fzfSidecar, error) {
				return fake, nil
			}
			fixture.dependencies.launchFZF = func(ctx context.Context, config fzf.Config) (fzf.Result, error) {
				return test.result(t, config, fixture), nil
			}

			outcome, err := RunPicker(context.Background(), fixture.options, fixture.dependencies)
			if err != nil || outcome.Status != test.status {
				t.Fatalf("RunPicker() outcome=%+v err=%v, want status %q", outcome, err, test.status)
			}
		})
	}
}

func TestRunPickerSidecarWaitErrorFollowsPickerErrorPrecedence(t *testing.T) {
	waitErr := errors.New("sidecar wait programming failure")
	launchErr := errors.New("fzf launch failure")
	for _, test := range []struct {
		name      string
		result    fzf.Result
		launchErr error
		wantErr   error
	}{
		{name: "outcome", result: fzf.Result{Aborted: true, ExitCode: 130}, wantErr: waitErr},
		{name: "picker error", launchErr: launchErr, wantErr: launchErr},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPickerFixture(t, protocol.PickerCP)
			fixture.dependencies.Environment = append(fixture.dependencies.Environment, fzfsidecar.ActivationVariable+"=1")
			fake := newAppSidecar("127.0.0.1:4321", "sidecar-key")
			fake.waitErr = waitErr
			fixture.dependencies.newFZFSidecar = func(protocol.Picker) (fzfSidecar, error) {
				return fake, nil
			}
			fixture.dependencies.launchFZF = func(context.Context, fzf.Config) (fzf.Result, error) {
				return test.result, test.launchErr
			}

			_, err := RunPicker(context.Background(), fixture.options, fixture.dependencies)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("RunPicker() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestRunPickerSidecarCredentialsStayOutOfTraceAndFZFArguments(t *testing.T) {
	fixture := newPickerFixture(t, protocol.PickerCP)
	fixture.options.TracePath = filepath.Join(t.TempDir(), "picker.trace.jsonl")
	fixture.dependencies.Environment = append(fixture.dependencies.Environment, fzfsidecar.ActivationVariable+"=1")
	fake := newAppSidecar("127.0.0.1:4321", "sidecar-secret")
	fixture.dependencies.newFZFSidecar = func(protocol.Picker) (fzfSidecar, error) {
		return fake, nil
	}
	fixture.dependencies.launchFZF = func(_ context.Context, config fzf.Config) (fzf.Result, error) {
		for _, argument := range config.Options {
			if strings.Contains(argument, fake.key) {
				t.Fatalf("sidecar key reached fzf argument %q", argument)
			}
		}
		return fzf.Result{Aborted: true, ExitCode: 130}, nil
	}

	if _, err := RunPicker(context.Background(), fixture.options, fixture.dependencies); err != nil {
		t.Fatalf("RunPicker() error = %v", err)
	}
	trace, err := os.ReadFile(fixture.options.TracePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{fake.key, fzfsidecar.ActivationVariable} {
		if strings.Contains(string(trace), secret) {
			t.Fatalf("trace contains %q: %s", secret, trace)
		}
	}
}

type appSidecar struct {
	address   string
	key       string
	events    []string
	wait      func()
	waitErr   error
	stopCalls int
	waitCalls int
}

func newAppSidecar(address, key string) *appSidecar {
	return &appSidecar{address: address, key: key}
}

func (sidecar *appSidecar) Address() string { return sidecar.address }

func (sidecar *appSidecar) APIKey() string { return sidecar.key }

func (sidecar *appSidecar) Start(context.Context) { sidecar.events = append(sidecar.events, "start") }

func (sidecar *appSidecar) Stop() {
	sidecar.stopCalls++
	sidecar.events = append(sidecar.events, "stop")
}

func (sidecar *appSidecar) Wait() error {
	sidecar.waitCalls++
	if sidecar.wait != nil {
		sidecar.wait()
	}
	return sidecar.waitErr
}

func containsPrefix(values []string, prefix string) bool {
	for _, value := range values {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}
