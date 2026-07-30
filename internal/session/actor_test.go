package session

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/pathutil"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

type generated struct {
	result candidate.BuildResult
	err    error
}

type generatorCall struct {
	ctx     context.Context
	request candidate.BuildRequest
	finish  chan generated
}

type controlledGenerator struct {
	started chan *generatorCall
}

func newControlledGenerator() *controlledGenerator {
	return &controlledGenerator{started: make(chan *generatorCall, 8)}
}

func (g *controlledGenerator) Generate(ctx context.Context, request candidate.BuildRequest) (candidate.BuildResult, error) {
	call := &generatorCall{ctx: ctx, request: request, finish: make(chan generated, 1)}
	g.started <- call
	completed := <-call.finish
	return completed.result, completed.err
}

func (g *controlledGenerator) Next(t *testing.T) *generatorCall {
	t.Helper()
	select {
	case call := <-g.started:
		return call
	case <-time.After(2 * time.Second):
		t.Fatal("generator did not start")
		return nil
	}
}

func (call *generatorCall) Complete(records []candidate.Record, err error) {
	call.finish <- generated{result: candidate.BuildResult{
		Records: records,
		Metrics: candidate.SourceMetrics{LocalDuration: time.Millisecond, ZoxideOutcome: "cached"},
	}, err: err}
}

type applyOutcome struct {
	result TransitionResult
	err    error
}

func asyncApply(actor *Actor, ctx context.Context, proposal ProposedTransition) <-chan applyOutcome {
	done := make(chan applyOutcome, 1)
	go func() {
		result, err := actor.Apply(ctx, proposal)
		done <- applyOutcome{result: result, err: err}
	}()
	return done
}

func awaitApply(t *testing.T, done <-chan applyOutcome) applyOutcome {
	t.Helper()
	select {
	case outcome := <-done:
		return outcome
	case <-time.After(2 * time.Second):
		t.Fatal("Apply did not reply")
		return applyOutcome{}
	}
}

func assertApplyPending(t *testing.T, done <-chan applyOutcome) {
	t.Helper()
	select {
	case outcome := <-done:
		t.Fatalf("Apply replied early: %+v", outcome)
	default:
	}
}

func testState(location string, mode protocol.Mode, prompt string) State {
	return State{
		Picker:   protocol.PickerCD,
		Mode:     mode,
		Location: pathutil.Filesystem([]byte(location)),
		Home:     pathutil.Filesystem([]byte("/home/test")),
		Prompt:   prompt,
	}
}

func testRecord(display, path string) candidate.Record {
	pathBytes := []byte(path)
	return candidate.Record{
		Kind:    protocol.KindDirectory,
		Display: display,
		Path:    pathBytes,
		Payload: protocol.EncodePath(pathBytes),
		Target:  pathutil.Filesystem(pathBytes),
	}
}

func testProposal(base uint64, state State, build bool, effect protocol.Effect) ProposedTransition {
	proposal := ProposedTransition{BaseGeneration: base, State: state, Effect: effect}
	if build {
		proposal.Build = &candidate.BuildRequest{Picker: state.Picker, Location: state.Location}
	}
	return proposal
}

func initializeActor(t *testing.T) (*Actor, *controlledGenerator) {
	t.Helper()
	generator := newControlledGenerator()
	actor := New(context.Background(), generator.Generate)
	t.Cleanup(func() {
		if err := actor.Close(); err != nil {
			t.Errorf("Close() = %v", err)
		}
	})
	pending := asyncApply(actor, context.Background(), testProposal(0, testState("/start", protocol.ModeInsert, "[I] "), true, protocol.Effect{}))
	call := generator.Next(t)
	call.Complete([]candidate.Record{testRecord("start", "/start")}, nil)
	if outcome := awaitApply(t, pending); outcome.err != nil {
		t.Fatalf("initialize Apply() = %v", outcome.err)
	}
	return actor, generator
}

func TestActorAssignsMonotonicGenerationOwnershipAcrossQueuedReplacement(t *testing.T) {
	generator := newControlledGenerator()
	actor := New(context.Background(), generator.Generate)
	t.Cleanup(func() { _ = actor.Close() })

	initialDone := asyncApply(actor, context.Background(), testProposal(0, testState("/start", protocol.ModeInsert, "start"), true, protocol.Effect{}))
	initial := generator.Next(t)
	if initial.request.Generation != 1 {
		t.Fatalf("initial generation=%d want 1", initial.request.Generation)
	}
	initial.Complete([]candidate.Record{testRecord("start", "/start")}, nil)
	if outcome := awaitApply(t, initialDone); outcome.err != nil || outcome.result.Snapshot.Generation() != 1 {
		t.Fatalf("initial outcome=%+v", outcome)
	}

	retiringDone := asyncApply(actor, context.Background(), testProposal(1, testState("/retiring", protocol.ModeNormal, "retiring"), true, protocol.Effect{}))
	retiring := generator.Next(t)
	if retiring.request.Generation != 2 {
		t.Fatalf("retiring generation=%d want 2", retiring.request.Generation)
	}
	replacementDone := asyncApply(actor, context.Background(), testProposal(1, testState("/replacement", protocol.ModeNormal, "replacement"), true, protocol.Effect{}))
	select {
	case <-retiring.ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("queued replacement did not retire active generation")
	}
	retiring.Complete(nil, context.Cause(retiring.ctx))
	if outcome := awaitApply(t, retiringDone); !errors.Is(outcome.err, ErrSuperseded) {
		t.Fatalf("retiring outcome=%+v", outcome)
	}

	replacement := generator.Next(t)
	if replacement.request.Generation != 3 {
		t.Fatalf("replacement generation=%d want 3", replacement.request.Generation)
	}
	replacement.Complete([]candidate.Record{testRecord("replacement", "/replacement")}, nil)
	outcome := awaitApply(t, replacementDone)
	if outcome.err != nil || outcome.result.Snapshot.Generation() != 3 || outcome.result.Effect.ReloadGeneration != 3 {
		t.Fatalf("replacement outcome=%+v", outcome)
	}
}

func TestCloneProposalPreservesBuildGenerationAndClonesLocation(t *testing.T) {
	proposal := testProposal(4, testState("/source", protocol.ModeNormal, "source"), true, protocol.Effect{})
	proposal.Build.Generation = 9
	cloned := cloneProposal(proposal)
	proposal.Build.Location.Path[0] = 'X'
	if cloned.Build.Generation != 9 || string(cloned.Build.Location.Path) != "/source" {
		t.Fatalf("cloned build=%+v", cloned.Build)
	}
}

func TestActorKeepsReadsLiveAndPublishesCompleteProposalAtomically(t *testing.T) {
	actor, generator := initializeActor(t)
	effect := protocol.Effect{ClearQuery: true, ClearMulti: true}
	pending := asyncApply(actor, context.Background(), testProposal(1, testState("/next", protocol.ModeNormal, "[N] "), true, effect))
	call := generator.Next(t)

	current, err := actor.Current(context.Background())
	if err != nil {
		t.Fatalf("Current() = %v", err)
	}
	if current.Generation() != 1 || string(current.State().Location.Path) != "/start" || current.State().Prompt != "[I] " {
		t.Fatalf("pending Current() = %+v", current)
	}
	assertApplyPending(t, pending)

	call.Complete([]candidate.Record{testRecord("next", "/next")}, nil)
	outcome := awaitApply(t, pending)
	if outcome.err != nil {
		t.Fatalf("Apply() = %v", outcome.err)
	}
	result := outcome.result
	if result.Snapshot.Generation() != 2 || result.Snapshot.State().Mode != protocol.ModeNormal ||
		result.Snapshot.State().Prompt != "[N] " || result.Snapshot.Records()[0].Display != "next" ||
		result.Effect.ReloadGeneration != 2 || !result.Effect.ClearQuery || !result.Effect.ClearMulti {
		t.Fatalf("result = %+v", result)
	}
	if result.Metrics.Sources.ZoxideOutcome != "cached" || result.Metrics.TransformDuration <= 0 {
		t.Fatalf("metrics = %+v", result.Metrics)
	}
}

func TestActorFailureAndMaliciousSupersedeDiscardWholeProposal(t *testing.T) {
	actor, generator := initializeActor(t)
	first := asyncApply(actor, context.Background(), testProposal(1, testState("/one", protocol.ModeNormal, "one"), true, protocol.Effect{Accept: true}))
	firstCall := generator.Next(t)
	second := asyncApply(actor, context.Background(), testProposal(1, testState("/two", protocol.ModeNormal, "two"), true, protocol.Effect{ClearQuery: true}))

	select {
	case <-firstCall.ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("superseded generator was not cancelled")
	}
	assertApplyPending(t, first)
	assertApplyPending(t, second)
	third := asyncApply(actor, context.Background(), testProposal(1, testState("/three", protocol.ModeNormal, "three"), true, protocol.Effect{}))
	if err := awaitApply(t, third).err; !errors.Is(err, ErrTransitionPending) {
		t.Fatalf("third Apply() = %v", err)
	}

	firstCall.Complete(nil, context.Canceled)
	if err := awaitApply(t, first).err; !errors.Is(err, ErrSuperseded) {
		t.Fatalf("first Apply() = %v", err)
	}
	secondCall := generator.Next(t)
	if got := string(secondCall.request.Location.Path); got != "/two" {
		t.Fatalf("replacement location = %q", got)
	}
	secondCall.Complete(nil, fs.ErrPermission)
	if err := awaitApply(t, second).err; !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("second Apply() = %v", err)
	}

	current, err := actor.Current(context.Background())
	if err != nil || current.Generation() != 1 || string(current.State().Location.Path) != "/start" || current.Records()[0].Display != "start" {
		t.Fatalf("Current() = %+v, %v", current, err)
	}
	stale := testProposal(0, testState("/stale", protocol.ModeNormal, "stale"), false, protocol.Effect{})
	if _, err := actor.Apply(context.Background(), stale); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("stale Apply() = %v", err)
	}
}

func TestActorNeverRollsBackCreatedTreeWhileGeneratorCanReadIt(t *testing.T) {
	actor, generator := initializeActor(t)
	root := t.TempDir()
	createdPath := filepath.Join(root, "created")
	if err := os.Mkdir(createdPath, 0o700); err != nil {
		t.Fatal(err)
	}
	created := &pathutil.CreatedTree{
		Target:  pathutil.Filesystem([]byte(createdPath)),
		Created: [][]byte{[]byte(createdPath)},
	}
	proposal := testProposal(1, testState(createdPath, protocol.ModeNormal, "created"), true, protocol.Effect{})
	proposal.Created = created
	ctx, cancel := context.WithCancel(context.Background())
	pending := asyncApply(actor, ctx, proposal)
	call := generator.Next(t)
	cancel()
	select {
	case <-call.ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("generator was not cancelled")
	}
	assertApplyPending(t, pending)
	if _, err := os.Stat(createdPath); err != nil {
		t.Fatalf("created tree removed while generator live: %v", err)
	}

	call.Complete(nil, context.Canceled)
	if err := awaitApply(t, pending).err; !errors.Is(err, context.Canceled) {
		t.Fatalf("Apply() = %v", err)
	}
	if _, err := os.Stat(createdPath); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("created tree was not rolled back: %v", err)
	}
}

func TestSnapshotRecordsAreImmutableCopies(t *testing.T) {
	actor, generator := initializeActor(t)
	virtual := candidate.Record{
		Kind: protocol.KindVirtual, Display: "Drives", Payload: protocol.EncodePath([]byte(protocol.VirtualDrivesTarget)),
		Target: pathutil.Drives(),
	}
	pending := asyncApply(actor, context.Background(), testProposal(1, testState("/records", protocol.ModeNormal, "records"), true, protocol.Effect{}))
	call := generator.Next(t)
	record := testRecord("one", "/one")
	call.Complete([]candidate.Record{record, virtual}, nil)
	if outcome := awaitApply(t, pending); outcome.err != nil {
		t.Fatal(outcome.err)
	}

	snapshot, err := actor.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	state := snapshot.State()
	state.Location.Path[0] = 'X'
	state.Home.Path[0] = 'X'
	records := snapshot.Records()
	records[0].Display = "changed"
	records[0].Path[0] = 'X'
	records[0].Target.Path[0] = 'X'
	records[1].Target.Kind = pathutil.KindFilesystem

	again, err := actor.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	gotState, gotRecords := again.State(), again.Records()
	if string(gotState.Location.Path) != "/records" || string(gotState.Home.Path) != "/home/test" ||
		gotRecords[0].Display != "one" || string(gotRecords[0].Path) != "/one" ||
		string(gotRecords[0].Target.Path) != "/one" || gotRecords[1].Target.Kind != pathutil.KindDrives {
		t.Fatalf("snapshot aliases actor storage: state=%+v records=%+v", gotState, gotRecords)
	}
}

func TestActorNilBuildRetainsGenerationRecordsAndResolvesCurrentMembership(t *testing.T) {
	actor, _ := initializeActor(t)
	proposal := testProposal(1, testState("/start", protocol.ModeNormal, "[N] "), false, protocol.Effect{Mode: protocol.ModeNormal})
	result, err := actor.Apply(context.Background(), proposal)
	if err != nil {
		t.Fatalf("Apply() = %v", err)
	}
	if result.Snapshot.Generation() != 1 || result.Snapshot.Records()[0].Display != "start" || result.Effect.ReloadGeneration != 0 {
		t.Fatalf("result = %+v", result)
	}
	if _, err := actor.Snapshot(context.Background(), 2); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("Snapshot(stale) = %v", err)
	}
	record := result.Snapshot.Records()[0]
	resolved, err := actor.ResolveCurrent(context.Background(), []byte(record.FullKey()))
	if err != nil || resolved.Display != "start" {
		t.Fatalf("ResolveCurrent() = %+v, %v", resolved, err)
	}
	resolved.Path[0] = 'X'
	again, _ := actor.ResolveCurrent(context.Background(), []byte(record.FullKey()))
	if string(again.Path) != "/start" {
		t.Fatalf("resolved record aliases actor storage: %q", again.Path)
	}
	if _, err := actor.ResolveCurrent(context.Background(), []byte("forged")); !errors.Is(err, ErrUnknownRecord) {
		t.Fatalf("ResolveCurrent(forged) = %v", err)
	}
}

func TestActorSessionCancellationWaitsForGenerationBeforeReplyAndRollback(t *testing.T) {
	sessionCtx, stop := context.WithCancelCause(context.Background())
	generator := newControlledGenerator()
	actor := New(sessionCtx, generator.Generate)
	root := t.TempDir()
	createdPath := filepath.Join(root, "created")
	if err := os.Mkdir(createdPath, 0o700); err != nil {
		t.Fatal(err)
	}
	created := &pathutil.CreatedTree{Target: pathutil.Filesystem([]byte(createdPath)), Created: [][]byte{[]byte(createdPath)}}
	proposal := testProposal(0, testState(createdPath, protocol.ModeNormal, "created"), true, protocol.Effect{})
	proposal.Created = created
	pending := asyncApply(actor, context.Background(), proposal)
	call := generator.Next(t)
	wantErr := errors.New("session stopped")
	stop(wantErr)
	select {
	case <-call.ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("generator was not cancelled")
	}
	assertApplyPending(t, pending)
	if _, err := os.Stat(createdPath); err != nil {
		t.Fatalf("tree removed before generator completion: %v", err)
	}
	call.Complete(nil, context.Canceled)
	if err := awaitApply(t, pending).err; !errors.Is(err, wantErr) {
		t.Fatalf("Apply() = %v", err)
	}
	if err := actor.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
}

func TestActorCloseWaitsForGenerationAndIsIdempotent(t *testing.T) {
	generator := newControlledGenerator()
	actor := New(context.Background(), generator.Generate)
	pending := asyncApply(actor, context.Background(), testProposal(0, testState("/pending", protocol.ModeNormal, "pending"), true, protocol.Effect{}))
	call := generator.Next(t)
	closed := make(chan error, 1)
	go func() { closed <- actor.Close() }()
	select {
	case <-call.ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not cancel generator")
	}
	select {
	case err := <-closed:
		t.Fatalf("Close returned before generator completion: %v", err)
	default:
	}
	assertApplyPending(t, pending)
	if _, err := actor.Current(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("Current during Close = %v", err)
	}
	call.Complete(nil, context.Canceled)
	if err := awaitApply(t, pending).err; !errors.Is(err, ErrClosed) {
		t.Fatalf("Apply() = %v", err)
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close() = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not return")
	}
	if err := actor.Close(); err != nil {
		t.Fatalf("second Close() = %v", err)
	}
	if _, err := actor.Current(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("Current after Close = %v", err)
	}
}
