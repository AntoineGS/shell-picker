package integration

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestRealZoxideHelperPublishesOnlyAfterRelease(t *testing.T) {
	root := t.TempDir()
	started := filepath.Join(root, "started")
	release := filepath.Join(root, "release")
	command := exec.Command(os.Args[0], "query", "--list")
	command.Env = replaceEnvironment(os.Environ(),
		parityHelperEnvironment+"="+realZoxideHelperMode,
		realZoxideStartedEnvironment+"="+started,
		realZoxideReleaseEnvironment+"="+release,
		realZoxideRootEnvironment+"="+root,
	)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = command.Process.Kill()
		_ = command.Wait()
	})
	if _, err := waitForRealZoxideFile(testContext(t), started); err != nil {
		t.Fatalf("helper start: %v", err)
	}
	if err := writeRealZoxideMarker(release, "release\n"); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("helper wait: %v; stderr=%q", err, stderr.Bytes())
	}
	want := filepath.Join(root, "late-match-target") + "\n" + filepath.Join(root, "other-target") + "\n"
	if stdout.String() != want {
		t.Fatalf("helper output=%q want %q", stdout.String(), want)
	}
}

func TestRealZoxideHelperCanBeCancelledBeforeRelease(t *testing.T) {
	root := t.TempDir()
	started := filepath.Join(root, "started")
	command := exec.Command(os.Args[0], "query", "--list")
	command.Env = replaceEnvironment(os.Environ(),
		parityHelperEnvironment+"="+realZoxideHelperMode,
		realZoxideStartedEnvironment+"="+started,
		realZoxideReleaseEnvironment+"="+filepath.Join(root, "release"),
		realZoxideRootEnvironment+"="+root,
	)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = command.Process.Kill()
		_ = command.Wait()
	})
	if _, err := waitForRealZoxideFile(testContext(t), started); err != nil {
		t.Fatalf("helper start: %v", err)
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err == nil {
		t.Fatal("cancelled helper exited successfully")
	}
}

func TestWaitForRealZoxideReleaseAcceptsAnExistingMarker(t *testing.T) {
	release := filepath.Join(t.TempDir(), "release")
	if err := os.WriteFile(release, []byte("release\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := waitForRealZoxideRelease(context.Background(), release); err != nil {
		t.Fatalf("wait for existing release marker: %v", err)
	}
}

func TestWaitForRealZoxideReleaseHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := waitForRealZoxideRelease(ctx, filepath.Join(t.TempDir(), "release"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("wait error=%v, want context cancellation", err)
	}
}

func TestRealFZFCDLateQueryFilteredArrival(t *testing.T) {
	fixture := newRealZoxideFixture(t, requireRealFZF(t))
	term, identity := fixture.StartBlockedCD(t, false, "local-visible")
	term.AssertProcessTopology(t)
	tracked := term.TrackLiveDescendants(t)

	beforeQuery := len(term.Output())
	if err := term.Send([]byte("late-match")); err != nil {
		t.Fatal(err)
	}
	waitForTerminalTextAfter(t, term, beforeQuery, "late-match")
	if bytes.Contains(visibleTerminalOutput(term.Output()[beforeQuery:]), []byte(filepath.Base(fixture.lateTarget))) {
		t.Fatal("late zoxide row appeared before the release marker")
	}

	beforeRelease := len(term.Output())
	fixture.Release(t)
	enrichment := term.WaitBarrier(testContext(t), barrier{Event: "zoxide.enrichment", Operation: "published", Count: 1})
	waitForTerminalTextAfter(t, term, beforeRelease, filepath.Base(fixture.lateTarget))
	beforeFinal := waitForRealFZFResultFinal(t, term, 2)
	waitForTerminalTextAfter(t, term, beforeFinal, filepath.Base(fixture.lateTarget))
	if bytes.Contains(visibleTerminalOutput(term.Output()[beforeFinal:]), []byte(filepath.Base(fixture.otherTarget))) {
		t.Fatalf("nonmatching late row appeared in the filtered view: %q", term.Output()[beforeFinal:])
	}
	if err := term.Send(keyEnter); err != nil {
		t.Fatal(err)
	}
	if err := term.Wait(testContext(t)); err != nil {
		t.Fatalf("picker wait: %v", err)
	}
	assertRealZoxideAccepted(t, term, fixture.lateTarget)
	if err := identity.WaitGone(testContext(t)); err != nil {
		t.Fatalf("zoxide process remained live: %v", err)
	}
	term.AssertTrackedProcessesGone(t, tracked)
	events := term.TraceEvents()
	assertRealZoxideGenerationSequence(t, events, 1)
	assertRealZoxideEnrichmentTrace(t, events, "published", 2, "ok")
	if enrichment.Generation != 2 || enrichment.CandidateCount != 5 {
		t.Fatalf("published enrichment=%+v", enrichment)
	}
}

func TestRealFZFNavigationDiscardsBlockedEnrichment(t *testing.T) {
	fixture := newRealZoxideFixture(t, requireRealFZF(t))
	fixture.ReplaceLocalCandidates(t, "local-child")
	if err := os.Mkdir(filepath.Join(fixture.cwd, "local-child", "inside-child"), 0o700); err != nil {
		t.Fatal(err)
	}
	term, identity := fixture.StartBlockedCD(t, true, "local-child")

	beforeQuery := len(term.Output())
	previewDispatchBeforeQuery := traceCount(term.TraceEvents(), "preview.dispatch", "")
	previewFinishedBeforeQuery := traceCount(term.TraceEvents(), "preview.finished", "")
	if err := term.Send([]byte("local-child")); err != nil {
		t.Fatal(err)
	}
	term.WaitBarrier(testContext(t), barrier{Event: "preview.dispatch", Count: previewDispatchBeforeQuery + 1})
	term.WaitBarrier(testContext(t), barrier{Event: "preview.finished", Operation: "ok", Renderer: "eza", Count: previewFinishedBeforeQuery + 1})
	waitForTerminalTextAfter(t, term, beforeQuery, "local-child")
	sendAndWait(t, term, keyRight, barrier{Event: "generation.publish", Generation: 2, Count: 1})
	term.WaitBarrier(testContext(t), barrier{Event: "callback.load", Operation: "ok", Generation: 2, Count: 1})
	waitForTerminalText(t, term, "inside-child")
	tracked := term.TrackLiveDescendants(t)

	beforeRelease := len(term.Output())
	fixture.Release(t)
	discarded := term.WaitBarrier(testContext(t), barrier{Event: "zoxide.enrichment", Operation: "discarded", Count: 1})
	if discarded.Generation != 1 || discarded.CandidateCount != 0 {
		t.Fatalf("discarded enrichment=%+v", discarded)
	}
	beforeReturn := len(term.Output())
	returnGeneration := sendAndWait(t, term, keyLeft, barrier{Event: "generation.publish", Generation: 3, Count: 1})
	term.WaitBarrier(testContext(t), barrier{Event: "callback.load", Operation: "ok", Generation: 3, Count: 1})
	waitForTerminalTextAfter(t, term, beforeReturn, filepath.Base(fixture.cwd))
	waitForTerminalTextAfter(t, term, beforeReturn, "local-child")
	if output := visibleTerminalOutput(term.Output()[beforeRelease:]); bytes.Contains(output, []byte(filepath.Base(fixture.lateTarget))) ||
		bytes.Contains(output, []byte(filepath.Base(fixture.otherTarget))) {
		t.Fatalf("late zoxide row appeared after navigation: %q", output)
	}

	if err := identity.WaitGone(testContext(t)); err != nil {
		t.Fatalf("zoxide process remained live: %v", err)
	}
	events := term.TraceEvents()
	assertRealZoxideGenerationSequence(t, events, 1, 2, 3)
	assertRealZoxideDiscardedTrace(t, events, 1, "cancelled")
	assertRealZoxideNavigationGenerationsNotRun(t, events, 2, 3)
	if returnGeneration.CandidateCount != 3 {
		t.Fatalf("return generation=%+v", returnGeneration)
	}
	loadBeforeEscape := traceCountGeneration(term.TraceEvents(), "callback.load", 3)
	beforeNormal := len(term.Output())
	if err := term.Send(keyEsc); err != nil {
		t.Fatal(err)
	}
	term.WaitBarrier(testContext(t), barrier{Event: "callback.event", Operation: "es", Count: 1})
	term.WaitBarrier(testContext(t), barrier{Event: "callback.load", Generation: 3, Count: loadBeforeEscape + 1})
	waitForTerminalTextAfter(t, term, beforeNormal, "[N]")
	if err := term.Send(keyEsc); err != nil {
		t.Fatal(err)
	}
	term.WaitBarrier(testContext(t), barrier{Event: "callback.event", Operation: "es", Count: 2})
	waitForTerminalText(t, term, promptReturnSentinel)
	waitErr := term.Wait(testContext(t))
	closed := term.WaitBarrier(testContext(t), barrier{Event: "session.close", Count: 1})
	if err := validateRealFZFAbort(waitErr, closed, term.ResultBytes()); err != nil {
		t.Fatalf("navigation abort: %v; trace=%+v", err, term.TraceEvents())
	}
	term.AssertTrackedProcessesGone(t, tracked)
}

func TestRealFZFAbortCancelsBlockedEnrichment(t *testing.T) {
	fixture := newRealZoxideFixture(t, requireRealFZF(t))
	term, identity := fixture.StartBlockedCD(t, true, "local-visible")
	tracked := term.TrackLiveDescendants(t)

	beforeNormal := len(term.Output())
	sendAndWait(t, term, keyEsc, barrier{Event: "callback.event", Operation: "es", Count: 1})
	waitForTerminalTextAfter(t, term, beforeNormal, "[N]")
	if err := term.Send(keyEsc); err != nil {
		t.Fatal(err)
	}
	term.WaitBarrier(testContext(t), barrier{Event: "callback.event", Operation: "es", Count: 2})
	waitForTerminalText(t, term, promptReturnSentinel)
	waitErr := term.Wait(testContext(t))
	closed := term.WaitBarrier(testContext(t), barrier{Event: "session.close", Count: 1})
	if err := validateRealFZFAbort(waitErr, closed, term.ResultBytes()); err != nil {
		t.Fatalf("abort: %v; trace=%+v", err, term.TraceEvents())
	}
	if err := identity.WaitGone(testContext(t)); err != nil {
		t.Fatalf("zoxide process remained live after abort: %v", err)
	}
	term.AssertTrackedProcessesGone(t, tracked)
	events := term.TraceEvents()
	assertRealZoxideGenerationSequence(t, events, 1)
	assertRealZoxideDiscardedTrace(t, events, 1, "cancelled")
}
