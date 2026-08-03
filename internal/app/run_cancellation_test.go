//go:build !windows

package app

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/fzf"
	"github.com/AntoineGS/shell-picker/internal/process"
	"github.com/AntoineGS/shell-picker/internal/protocol"
	"github.com/AntoineGS/shell-picker/internal/sessionipc"
)

func TestRunPickerLaunchesFZFBeforeInitialZoxideCompletes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX executable fixture")
	}
	fixture := newPickerFixture(t, protocol.PickerCD)
	fixture.options.ZoxidePolicy = candidate.ZoxideFresh
	fixture.options.ZoxideTimeout = 0
	fixture.dependencies.ZoxidePath = zoxideFixture(t, fixture.child)
	beforeStart := make(chan struct{})
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	counts := newProcessCounts()
	fixture.dependencies.ProcessRunner.Observe = func(event process.ProcessEvent) {
		counts.observe(event)
		if event.Phase == "start" {
			started <- struct{}{}
		}
	}
	fixture.dependencies.ProcessRunner.BeforeStart = func(spec process.Spec) error {
		if spec.Path != fixture.dependencies.ZoxidePath {
			return nil
		}
		close(beforeStart)
		<-release
		return nil
	}
	launchChecked := make(chan error, 1)
	fixture.dependencies.launchFZF = func(_ context.Context, config fzf.Config) (fzf.Result, error) {
		select {
		case <-started:
			launchChecked <- errors.New("zoxide process started before fzf launch")
			return fzf.Result{}, errors.New("zoxide process started before fzf launch")
		default:
		}
		releaseOnce.Do(func() { close(release) })

		raw, err := readFramedRecord(config.Input)
		if err != nil {
			launchChecked <- fmt.Errorf("read local frame: %w", err)
			return fzf.Result{}, err
		}
		wire, err := protocol.ParseRecord(raw)
		if err != nil || wire.Kind != protocol.KindLocal {
			if err == nil {
				err = fmt.Errorf("first frame kind=%q, want local", wire.Kind)
			}
			launchChecked <- err
			return fzf.Result{}, err
		}
		if _, err := readFramedRecord(config.Input); err != nil {
			launchChecked <- fmt.Errorf("read late zoxide frame: %w", err)
			return fzf.Result{}, err
		}
		launchChecked <- nil
		return fzf.Result{Aborted: true, ExitCode: 130}, nil
	}

	done := make(chan error, 1)
	go func() {
		_, err := RunPicker(context.Background(), fixture.options, fixture.dependencies)
		done <- err
	}()
	select {
	case <-beforeStart:
	case <-time.After(2 * time.Second):
		t.Fatal("initial zoxide did not start")
	}
	select {
	case checkErr := <-launchChecked:
		if checkErr != nil {
			t.Fatal(checkErr)
		}
	case <-time.After(2 * time.Second):
		releaseOnce.Do(func() { close(release) })
		if err := <-done; err == nil {
			t.Fatal("RunPicker unexpectedly succeeded without launching fzf")
		}
		t.Fatal("fzf was not launched while zoxide was blocked")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	attempts, processStarts, maxLive, exits, live := counts.lifecycleValues()
	if attempts != 1 || processStarts != 1 || maxLive != 1 || exits != 1 || live != 0 {
		t.Fatalf("processes=(attempts=%d starts=%d maxLive=%d exits=%d live=%d)", attempts, processStarts, maxLive, exits, live)
	}
}

func TestRunPickerParentCancellationStopsLiveZoxideAfterFZFLaunch(t *testing.T) {
	fixture := newPickerFixture(t, protocol.PickerCD)
	fixture.options.ZoxidePolicy = candidate.ZoxideFresh
	fixture.options.ZoxideTimeout = 0
	fixture.dependencies.ZoxidePath = cancellationZoxideFixture(t, fixture.child)
	counts := newProcessCounts()
	started := make(chan struct{}, 1)
	fixture.dependencies.ProcessRunner.Observe = func(event process.ProcessEvent) {
		counts.observe(event)
		if event.Phase == "start" {
			started <- struct{}{}
		}
	}
	launched := make(chan struct{})
	fixture.dependencies.launchFZF = func(ctx context.Context, _ fzf.Config) (fzf.Result, error) {
		close(launched)
		<-ctx.Done()
		return fzf.Result{Aborted: true, ExitCode: 130}, nil
	}

	cause := errors.New("parent stopped picker")
	ctx, cancel := context.WithCancelCause(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := RunPicker(ctx, fixture.options, fixture.dependencies)
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("zoxide process did not start")
	}
	select {
	case <-launched:
	case <-time.After(2 * time.Second):
		t.Fatal("fzf was not launched")
	}
	cancel(cause)

	select {
	case err := <-done:
		if err != cause {
			t.Fatalf("RunPicker err=%v, want exact parent cause %v", err, cause)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("RunPicker did not join cancelled lifecycle")
	}
	attempts, processStarts, maxLive, exits, live := counts.lifecycleValues()
	if attempts != 1 || processStarts != 1 || maxLive != 1 || exits != 1 || live != 0 {
		t.Fatalf("processes=(attempts=%d starts=%d maxLive=%d exits=%d live=%d)", attempts, processStarts, maxLive, exits, live)
	}
}

func TestRunPickerStopsLiveZoxideWhenFZFReturns(t *testing.T) {
	launchErr := errors.New("fzf launch failed")
	for _, test := range []struct {
		name    string
		result  fzf.Result
		err     error
		status  protocol.Status
		wantErr error
	}{
		{name: "abort", result: fzf.Result{Aborted: true, ExitCode: 130}, status: protocol.StatusAborted},
		{name: "failure", err: launchErr, wantErr: launchErr},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPickerFixture(t, protocol.PickerCD)
			fixture.options.ZoxidePolicy = candidate.ZoxideFresh
			fixture.options.ZoxideTimeout = 0
			fixture.dependencies.ZoxidePath = cancellationZoxideFixture(t, fixture.child)
			counts := newProcessCounts()
			started := make(chan struct{}, 1)
			fixture.dependencies.ProcessRunner.Observe = func(event process.ProcessEvent) {
				counts.observe(event)
				if event.Phase == "start" {
					started <- struct{}{}
				}
			}
			fixture.dependencies.launchFZF = func(_ context.Context, _ fzf.Config) (fzf.Result, error) {
				select {
				case <-started:
				case <-time.After(2 * time.Second):
					return fzf.Result{}, errors.New("zoxide did not remain live through fzf launch")
				}
				return test.result, test.err
			}

			outcome, err := RunPicker(context.Background(), fixture.options, fixture.dependencies)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("err=%v, want %v", err, test.wantErr)
				}
			} else if err != nil || outcome.Status != test.status {
				t.Fatalf("outcome=%+v err=%v, want status %q", outcome, err, test.status)
			}
			attempts, processStarts, maxLive, exits, live := counts.lifecycleValues()
			if attempts != 1 || processStarts != 1 || maxLive != 1 || exits != 1 || live != 0 {
				t.Fatalf("processes=(attempts=%d starts=%d maxLive=%d exits=%d live=%d)", attempts, processStarts, maxLive, exits, live)
			}
		})
	}
}

func TestRunPickerInitialLocalCancellationJoinsStartedZoxide(t *testing.T) {
	fixture := newPickerFixture(t, protocol.PickerCD)
	fixture.options.ZoxidePolicy = candidate.ZoxideFresh
	fixture.options.ZoxideTimeout = 0
	fixture.dependencies.ZoxidePath = zoxideFixture(t, fixture.child)
	counts := newProcessCounts()
	zoxideBeforeStart := make(chan struct{})
	allowZoxideStart := make(chan struct{})
	zoxideStarted := make(chan struct{})
	localStarted := make(chan struct{})
	localRelease := make(chan struct{})
	var zoxideBeforeStartOnce, zoxideStartedOnce, localStartedOnce sync.Once
	fixture.dependencies.ProcessRunner.Observe = func(event process.ProcessEvent) {
		if event.Path != fixture.dependencies.ZoxidePath {
			return
		}
		counts.observe(event)
		if event.Phase == "start" {
			zoxideStartedOnce.Do(func() { close(zoxideStarted) })
		}
	}
	fixture.dependencies.ProcessRunner.BeforeStart = func(spec process.Spec) error {
		if spec.Path == fixture.dependencies.ZoxidePath {
			zoxideBeforeStartOnce.Do(func() { close(zoxideBeforeStart) })
			<-allowZoxideStart
		}
		return nil
	}
	fixture.dependencies.buildLocal = func(context.Context, candidate.BuildRequest) (candidate.BuildResult, error) {
		localStartedOnce.Do(func() { close(localStarted) })
		<-localRelease
		return candidate.BuildResult{Records: []candidate.Record{enrichmentRecord(protocol.KindLocal, fixture.child)}, Metrics: candidate.SourceMetrics{ZoxideOutcome: "not-run"}}, nil
	}
	launched := make(chan struct{}, 1)
	fixture.dependencies.launchFZF = func(context.Context, fzf.Config) (fzf.Result, error) {
		launched <- struct{}{}
		return fzf.Result{}, errors.New("fzf launched after initial local cancellation")
	}

	cause := errors.New("initial local build canceled")
	ctx, cancel := context.WithCancelCause(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := RunPicker(ctx, fixture.options, fixture.dependencies)
		done <- err
	}()
	select {
	case <-zoxideBeforeStart:
	case <-time.After(2 * time.Second):
		t.Fatal("zoxide did not reach start gate")
	}
	select {
	case <-localStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("local build did not reach cancellation gate")
	}
	close(allowZoxideStart)
	select {
	case <-zoxideStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("zoxide process did not start")
	}
	cancel(cause)
	close(localRelease)
	select {
	case err := <-done:
		if !errors.Is(err, cause) {
			t.Fatalf("RunPicker err=%v, want %v", err, cause)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("RunPicker did not join startup cancellation")
	}
	select {
	case <-launched:
		t.Fatal("fzf launched after initial local cancellation")
	default:
	}
	attempts, starts, maxLive, exits, live := counts.lifecycleValues()
	if attempts != 1 || starts != 1 || exits != 1 || live != 0 || maxLive != 1 {
		t.Fatalf("processes=(attempts=%d starts=%d maxLive=%d exits=%d live=%d)", attempts, starts, maxLive, exits, live)
	}
}

func TestRunPickerInitialPublicationCancellationPrefersParentCause(t *testing.T) {
	fixture := newPickerFixture(t, protocol.PickerCD)
	fixture.options.ZoxidePolicy = candidate.ZoxideFresh
	fixture.options.ZoxideTimeout = 0
	fixture.dependencies.ZoxidePath = cancellationZoxideFixture(t, fixture.child)
	counts := newProcessCounts()
	zoxideStarted := make(chan struct{})
	var zoxideStartedOnce sync.Once
	fixture.dependencies.ProcessRunner.Observe = func(event process.ProcessEvent) {
		if event.Path != fixture.dependencies.ZoxidePath {
			return
		}
		counts.observe(event)
		if event.Phase == "start" {
			zoxideStartedOnce.Do(func() { close(zoxideStarted) })
		}
	}
	fixture.dependencies.buildLocal = func(context.Context, candidate.BuildRequest) (candidate.BuildResult, error) {
		return candidate.BuildResult{
			Records: []candidate.Record{enrichmentRecord(protocol.KindLocal, fixture.child)},
			Metrics: candidate.SourceMetrics{ZoxideOutcome: "not-run"},
		}, nil
	}
	publicationReached := make(chan struct{})
	publicationRelease := make(chan struct{})
	var releaseOnce sync.Once
	releasePublication := func() { releaseOnce.Do(func() { close(publicationRelease) }) }
	streamReady := make(chan *fzf.InputStream, 1)
	fixture.dependencies.beforeInitialInputPublish = func(input *fzf.InputStream) {
		streamReady <- input
		close(publicationReached)
		<-publicationRelease
	}
	launched := make(chan struct{}, 1)
	fixture.dependencies.launchFZF = func(context.Context, fzf.Config) (fzf.Result, error) {
		launched <- struct{}{}
		return fzf.Result{}, errors.New("fzf launched after initial publication cancellation")
	}

	cause := errors.New("initial publication cancelled")
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(cause)
	defer releasePublication()
	done := make(chan error, 1)
	go func() {
		_, err := RunPicker(ctx, fixture.options, fixture.dependencies)
		done <- err
	}()

	select {
	case <-publicationReached:
	case <-time.After(2 * time.Second):
		t.Fatal("initial publication did not reach handoff")
	}
	stream := <-streamReady
	select {
	case <-zoxideStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("zoxide process did not start")
	}
	cancel(cause)
	streamClosed := make(chan error, 1)
	go func() {
		var buffer [1]byte
		_, err := stream.Read(buffer[:])
		streamClosed <- err
	}()
	select {
	case err := <-streamClosed:
		if !errors.Is(err, cause) {
			t.Fatalf("stream close error=%v, want %v", err, cause)
		}
	case <-time.After(2 * time.Second):
		releasePublication()
		t.Fatal("initial stream was not closed after parent cancellation")
	}
	releasePublication()

	select {
	case err := <-done:
		if err != cause {
			t.Fatalf("RunPicker err=%v, want %v", err, cause)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("RunPicker did not join initial cancellation")
	}
	select {
	case <-launched:
		t.Fatal("fzf launched after initial publication cancellation")
	default:
	}
	attempts, starts, maxLive, exits, live := counts.lifecycleValues()
	if attempts != 1 || starts != 1 || exits != 1 || live != 0 || maxLive != 1 {
		t.Fatalf("processes=(attempts=%d starts=%d maxLive=%d exits=%d live=%d)", attempts, starts, maxLive, exits, live)
	}
}

func TestRunPickerListenFailureJoinsStartedZoxide(t *testing.T) {
	fixture := newPickerFixture(t, protocol.PickerCD)
	fixture.options.ZoxidePolicy = candidate.ZoxideFresh
	fixture.options.ZoxideTimeout = 0
	fixture.dependencies.ZoxidePath = zoxideFixture(t, fixture.child)
	counts := newProcessCounts()
	zoxideBeforeStart := make(chan struct{})
	allowZoxideStart := make(chan struct{})
	zoxideStarted := make(chan struct{})
	localStarted := make(chan struct{})
	localRelease := make(chan struct{})
	var zoxideBeforeStartOnce, zoxideStartedOnce, localStartedOnce sync.Once
	fixture.dependencies.ProcessRunner.Observe = func(event process.ProcessEvent) {
		if event.Path != fixture.dependencies.ZoxidePath {
			return
		}
		counts.observe(event)
		if event.Phase == "start" {
			zoxideStartedOnce.Do(func() { close(zoxideStarted) })
		}
	}
	fixture.dependencies.ProcessRunner.BeforeStart = func(spec process.Spec) error {
		if spec.Path == fixture.dependencies.ZoxidePath {
			zoxideBeforeStartOnce.Do(func() { close(zoxideBeforeStart) })
			<-allowZoxideStart
		}
		return nil
	}
	fixture.dependencies.buildLocal = func(context.Context, candidate.BuildRequest) (candidate.BuildResult, error) {
		localStartedOnce.Do(func() { close(localStarted) })
		<-localRelease
		return candidate.BuildResult{Records: []candidate.Record{enrichmentRecord(protocol.KindLocal, fixture.child)}, Metrics: candidate.SourceMetrics{ZoxideOutcome: "not-run"}}, nil
	}
	listenErr := errors.New("IPC listen failed")
	fixture.dependencies.listenIPC = func(context.Context, sessionipc.Token, sessionipc.Backend) (*sessionipc.Server, error) {
		select {
		case <-zoxideStarted:
		default:
			return nil, errors.New("listen called before zoxide start")
		}
		return nil, listenErr
	}
	launched := make(chan struct{}, 1)
	fixture.dependencies.launchFZF = func(context.Context, fzf.Config) (fzf.Result, error) {
		launched <- struct{}{}
		return fzf.Result{}, errors.New("fzf launched after listen failure")
	}

	done := make(chan error, 1)
	go func() {
		_, err := RunPicker(context.Background(), fixture.options, fixture.dependencies)
		done <- err
	}()
	select {
	case <-zoxideBeforeStart:
	case <-time.After(2 * time.Second):
		t.Fatal("zoxide did not reach start gate")
	}
	select {
	case <-localStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("local build did not reach listen gate")
	}
	close(allowZoxideStart)
	select {
	case <-zoxideStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("zoxide process did not start")
	}
	close(localRelease)
	select {
	case err := <-done:
		if !errors.Is(err, listenErr) {
			t.Fatalf("RunPicker err=%v, want %v", err, listenErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("RunPicker did not join listen failure")
	}
	select {
	case <-launched:
		t.Fatal("fzf launched after listen failure")
	default:
	}
	attempts, starts, maxLive, exits, live := counts.lifecycleValues()
	if attempts != 1 || starts != 1 || exits != 1 || live != 0 || maxLive != 1 {
		t.Fatalf("processes=(attempts=%d starts=%d maxLive=%d exits=%d live=%d)", attempts, starts, maxLive, exits, live)
	}
}
