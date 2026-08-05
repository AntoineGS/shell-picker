package integration

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

const task20FocusedPattern = `^Test(Actor|Handle|Reduce|NormalEscape|Validate|Server|Client|FinishedTelemetry|CachedPolicy|Fresh|IndependentFresh|CPNever|CallerCancellation|BuilderZoxide|Zoxide|EnumerateReadDir|Kqueue|RejectsNonIdentifiable|WaitDelay|WaitIs|CancellationCloses|ExitErrorPrecedesBlocking|OrdinaryCompletion|Foreground|RestoreForeground|RetainedInherited|CancelKills|InheritedTree|Preview|Archive|Zip|Cache|Converter|Renderer|ExternalRenderer|Terminal|SessionSpec|PickerOptions|PrepareSession|Run|ParseOutput|ActionArgument|TokenCanary|PickerBackend|FZFShell|CreateDirectory|SecurityGate|Forged|CancelledNavigation|ResourceSnapshot|ParityPreview|RealFZF|Windows|CreateProcess|RunnerBeforeStart|RunnerNilBeforeStart|Enabled|NewProvides|DefaultTransport|ReadBody|Post|GetState|DecodeState|ActionBody|TypedState|Session|StopAndWait|Concurrent|ParentCancellation|Sidecar)`

const task20ManifestPackages = `./internal/session ./internal/sessionipc ./internal/candidate ./internal/process ./internal/callback ./internal/fzfsidecar ./internal/preview ./internal/fzf ./internal/app ./internal/pathutil ./integration`

type task20GatePackage struct {
	path                          string
	tests                         []string
	windows                       []string
	unix                          []string
	selectedUnix, selectedWindows int
}

var task20GateManifest = []task20GatePackage{
	{path: "./internal/session", selectedUnix: 42, selectedWindows: 45, tests: []string{
		"TestActorKeepsReadsLiveAndPublishesCompleteProposalAtomically",
		"TestActorAssignsMonotonicGenerationOwnershipAcrossQueuedReplacement",
		"TestActorCreatedTreeDiscardOrdering",
		"TestHandleAddCreateErrorsRetainAddSnapshotAndDoNotBuild",
		"TestHandleAddGenerationFailureRollsBackAndPreservesParents",
		"TestHandleAddRollbackWaitsForGenerationCompletion",
		"TestHandleTransfersActualCreatedTreeForStaleBaseRollback",
		"TestHandleAddPublishesCompleteStateAtomicallyAfterSingleGeneration",
		"TestReduceValidAddIsPureAndClonesIntent",
		"TestNormalEscapeAbortsWithoutChangingState",
		"TestValidateCDRequiresOneExactFilesystemDirectoryRecord",
		"TestValidateCPRejectsMalformedUnknownResidualAndVirtual",
	}},
	{path: "./internal/sessionipc", selectedUnix: 19, selectedWindows: 19, tests: []string{
		"TestServerRejectsNoncanonicalRouteTargetsBeforeBackend",
		"TestServerAcceptsOnlyOneExactRawAuthorizationValue",
		"TestServerRejectsChunkedRequestAt64KiBPlusOne",
		"TestServerRejectsSeventeenthHandlerAndCloseCancelsAndJoins",
		"TestServerCloseCancelsCooperativeBackend",
		"TestClientBoundsEveryResponseClassAtLimitAndLimitPlusOne",
		"TestClientClosesOverlimitBodyWithoutReusableTransport",
		"TestServerRejectsBackendReturnedVirtualPreview",
		"TestFinishedTelemetryExactChildBounds",
	}},
	{path: "./internal/candidate", selectedUnix: 22, selectedWindows: 23, tests: []string{
		"TestCachedPolicyNoninitialBuildIsLocalOnlyWithoutReadyCache",
		"TestFreshPolicyNoninitialBuildSkipsFactoryPermitAndProcess",
		"TestFreshZeroTimeoutIsAuthoritativeForInitialBuild",
		"TestFreshBuilderSerializesSessionQueriesAndCancelledWaiterDoesNotAttempt",
		"TestIndependentFreshSessionBuildersMayQueryConcurrently",
		"TestCPNeverLoadsCacheOrInvokesFreshFactory",
		"TestCallerCancellationBeforePrivateTimeoutWinsAndReaps",
		"TestBuilderZoxideTimeoutIsSoftAndDiscardsPartialOutput",
		"TestZoxideMalformedAndProcessFailuresDiscardCompleteBuffer",
		"TestZoxideMissingAndSpawnFailureAttemptWithoutStart",
	}, unix: []string{
		"TestEnumerateReadDirErrorPublishesNothing",
	}},
	{path: "./internal/process", selectedUnix: 25, selectedWindows: 32, tests: []string{
		"TestKqueueEventValidation",
		"TestKqueueRegistrationValidatedBeforeWait",
		"TestRejectsNonIdentifiableValueCloserBeforeAttempt",
		"TestWaitDelayClosesBlockingPumpedStreams",
		"TestCancellationClosesBlockingPumpedStream",
		"TestExitErrorPrecedesBlockingPumpWaitDelay",
		"TestOrdinaryCompletionDoesNotClosePumpedCloser",
		"TestWaitDelayClosesSharedPointerOnce",
		"TestWaitIsSingleUseAndWaitDelayBoundsInheritedPipe",
		"TestRunnerBeforeStartInspectsValidatedSpecAndCannotSynthesizeSuccess",
		"TestRunnerBeforeStartRunsOnlyAfterSpecValidation",
		"TestRunnerNilBeforeStartExecutesRealStart",
		"TestRunnerBeforeStartReturningNilCannotAvoidRealStart",
	}, windows: []string{
		"TestWindowsStartFailureStagesCloseEverything",
		"TestWindowsCancelTerminatesJob",
		"TestWindowsForegroundAndInheritedJobsTerminateDescendants",
		"TestWindowsRejectsExtraFilesBeforeProcessAttempt",
		"TestWindowsForegroundUsesOwnedJobWithoutTTY",
		"TestWindowsExitErrorPreservesWaitDelayClassification",
		"TestCreateProcessPassesStreamsAndArguments",
		"TestCreateProcessInheritsOnlyExplicitChildHandles",
	}, unix: []string{
		"TestCancelKillsOwnedProcessTreeEventually",
		"TestInheritedTreeCancellationKillsCallbackGroup",
		"TestForegroundTreeOwnsTTYAndRestoresPreviousGroup",
		"TestRestoreForegroundPGRRestoresMaskOnIoctlError",
		"TestRetainedInheritedTreeKillsGroupAfterChildWait",
	}},
	{path: "./internal/callback", selectedUnix: 8, selectedWindows: 8, tests: []string{
		"TestPreviewRejectsVirtualAndRelativeBeforeRenderer",
		"TestPreviewTerminalResourceSkipsFinishedTelemetry",
		"TestPreviewAggregatesSequentialChildTelemetry",
	}},
	{path: "./internal/fzfsidecar", selectedUnix: 64, selectedWindows: 64, tests: []string{
		"TestEnabledRequiresExactValue",
		"TestEnabledHandlesDuplicateActivationEntries",
		"TestEnabledUsesPlatformSpecificNameMatching",
		"TestEnabledRejectsMalformedEntriesAndDoesNotMutateInput",
		"TestNewProvidesNumericLoopbackAddressAndIndependentKeys",
		"TestReadBodyAcceptsLimitAndRejectsLimitPlusOneWhileClosing",
		"TestClientRequestsUseExactURLKeyAndContentPolicy",
		"TestDefaultTransportDisablesProxy",
		"TestClientRejectsRedirectsWithoutFollowingThem",
		"TestPostRejectsStatusAndClosesResponseBody",
		"TestPostResponseUsesTheSameBoundedBodyPolicy",
		"TestClientKeepsTypedPostTransportFailuresRawAndPost503Transient",
		"TestClientKeepsUnauthorizedAndValidationFailuresTerminal",
		"TestClientKeepsParentCancellationTerminal",
		"TestGetStatePaginatesFZFSelectedDumpInFixedPages",
		"TestGetStateCDUsesOnlyTheFirstSelectedPage",
		"TestGetStateRejectsCountMutationBetweenSelectedPages",
		"TestGetStateCancellationStopsSelectedPagination",
		"TestGetStateAppliesBodyLimitToEverySelectedPage",
		"TestGetStateRejectsMaxCountAndPageBoundViolations",
		"TestGetStateBoundsSelectedPaginationAtTheCyclePageCap",
		"TestGetStateStopsWhenTheAggregatePaginationContextExpires",
		"TestDecodeStateAllowsUnknownFieldsAndFormatsPickerCounts",
		"TestDecodeStateRequiresStrictIntegerCountsAndSelectedArray",
		"TestActionBodyUsesOnlyTheValidatedFZFActionGrammar",
		"TestPostStateConstructsActionFromTypedCountsNotCachedText",
		"TestPostStateRejectsInconsistentTypedCountsBeforeHTTP",
		"TestTypedStateValidationRejectsRelationalMismatches",
		"TestClientKeepsTypedGetTransportFailuresRaw",
		"TestSessionObserverIsDefaultDisabled",
		"TestSessionObserverEmitsClosedEventsWithCountersAndDurations",
		"TestSessionObserverIncludesSafeFailureMetadata",
		"TestSessionWaitsForFreshTimerAfterSlowPollingCycle",
		"TestSessionRepeatedStartOwnsOnePollingGoroutine",
		"TestSessionRetriesReadinessTransportFailuresUntilAValidState",
		"TestSessionRetriesStartupBusy503UntilStateIsStable",
		"TestSessionPostsInitialStateAndOnlyChangedLabels",
		"TestSessionDoesNotPostWhenRenderedLabelIsUnchanged",
		"TestSessionAuthenticatedFakeServerFailsSoftOnMalformedUnauthorizedForbiddenAndOversizedState",
		"TestStopAndWaitCloseTransportExactlyOnce",
		"TestConcurrentRepeatedLifecycleCallsAreIdempotent",
		"TestSessionCancellationUnblocksReadinessAndBlockedRequests",
		"TestSessionObserverSuppressesCanceledOperationEvents",
		"TestSessionRetriesRepeatedReadinessRefusalsUntilConfiguredDeadline",
		"TestSessionRetriesReadinessRequestTimeoutsUntilValidState",
		"TestSessionRetriesReadinessRequestTimeoutsUntilConfiguredDeadline",
		"TestSessionStopClosesAnActiveResponseBody",
		"TestParentCancellationClosesAnActiveResponseBody",
		"TestSessionRetries79TransientBusyCyclesBeforeValidState",
		"TestSessionSoftStopsWhenTransientBusyWindowExpires",
		"TestSessionResetsTransientWindowAfterValidState",
		"TestSessionStopReasonPreservesTheFirstCancellationCause",
		"TestSessionRetriesChangedStateAfterTransientPostUntilSuccess",
		"TestSessionRetriesReadyConnectionRefusalAsBoundedTransient",
		"TestSessionStopsWhenTransientPostWindowExpires",
		"TestSessionTransientWindowResetsAfterValidStateCycle",
		"TestSessionReadinessExpiryIsSoft",
		"TestSessionStopsSoftlyAfterAReadyStateFails",
		"TestSessionUsesTheDefaultIntervalAndStopsTickerOnce",
		"TestSessionWithPaginatedCPStatePostsOnlyChangedLabels",
		"TestSessionRetriesInconsistentPaginatedSnapshotOnTheNextTick",
		"TestSessionDoesNotPostWhenSelectedPaginationReachesThePageCap",
		"TestSessionStopAndWaitAreSafeBeforeStartAndRepeated",
		"TestConcurrentPreStartLifecycleCallsReachOneTerminalState",
	}},
	{path: "./internal/preview", selectedUnix: 32, selectedWindows: 39, tests: []string{
		"TestArchiveLimitsEntriesBytesAndDeadline",
		"TestZipPreflightRejectsCentralDirectoryBombBeforeArchiveOpen",
		"TestCachePutAnchorsRootAcrossSwap",
		"TestCacheConcurrentProductionPutHasImmutableSingleLinkWinner",
		"TestConverterFinalValidationRejectsOversizedArtifact",
		"TestRendererReadsValidatedStageWhenPathIsReplaced",
		"TestRendererFailuresAreWaitedSequentiallyBeforeNativeFallback",
		"TestTerminalRetriesBlockedCleanupAfterTreeKill",
	}, unix: []string{
		"TestExternalRendererOutputLimitKillsInheritedGroupWithoutFallback",
		"TestExternalRendererDeadlineKillsInheritedGroupWithoutFallback",
		"TestExternalRendererWaitDelayKillsInheritedGroupWithoutFallback",
	}},
	{path: "./internal/fzf", selectedUnix: 24, selectedWindows: 24, tests: []string{
		"TestPickerOptionsRejectListenAddressInjection",
		"TestPickerOptionsAcceptCanonicalListenAddresses",
		"TestPickerOptionsSidecarUsesTabForFZFSelectionAndKeepsForwardNavigationOnRight",
		"TestSessionSpecSeparatesCallbackCredentialsFromInheritedEnvironment",
		"TestSessionSpecDisabledPreservesInheritedFZFAPIKey",
		"TestSessionSpecEnabledOverridesInheritedFZFAPIKey",
		"TestPrepareSessionValidatesListenAPIKeyContract",
		"TestSessionSpecKeyCanaryDoesNotReachArgumentsErrorsOrObserver",
		"TestRunRejectsUnsafeConfigurationBeforeProcessStart",
		"TestRunDoesNotProbeVersion",
		"TestParseOutputRejectsMalformedFrames",
		"TestActionArgumentDelimiterCorpusCannotInjectAction",
	}},
	{path: "./internal/app", selectedUnix: 58, selectedWindows: 57, tests: []string{
		"TestFZFShellRejectsMissingOrExtraCommandText",
		"TestFZFShellCallbackUnsetsListenAPIKeyBeforeValidation",
		"TestFZFShellTransportFailureReturnsOneWithoutCredentials",
		"TestFZFShellRejectsEventKeyBeforeIPCClientConstruction",
		"TestPreviewEnvironmentAllowsOnlyPathLocaleAndTerminalMetadata",
		"TestTokenCanaryUsesActualCallbackCredentialAndExcludesNamedSinks",
		"TestPickerBackendRejectsAuthorizedVirtualBeforeFilesystemAndOutput",
		"TestPickerBackendCurrentHeaderEmitsDisplayTrace",
		"TestRunPickerClosesCallbackEndpointBeforeReturning",
		"TestRunPickerConcreteSidecarJoinsActivePostForAcceptedAndAborted",
		"TestRunPickerConcreteSidecarDoesNotForwardUnknownStateOrAPIKey",
		"TestSidecarObserverWritesOnlySchemaValidDiagnostics",
		"TestSidecarTraceObserverSuppressesCanceledOperationRecords",
		"TestRunPickerAppliesZoxidePolicyProcessBudgets",
		"TestRunPickerReturnsHardErrorWhenLoadRequestFailsBeforeBegin",
		"TestRunPickerNavigationRemovesZoxideCandidates",
		"TestRunPickerCompactsZoxideHomeDisplay",
		"TestRunPickerSidecarCredentialsStayOutOfTraceAndFZFArguments",
	}, windows: []string{
		"TestWindowsTraceMutexNameHashesCanonicalSinkIdentity",
		"TestWindowsTraceSinkSessionInitialization",
		"TestWindowsTraceRecordLockWaitIsBounded",
		"TestWindowsTraceRecordWriterAcceptsAbandonedMutexAndRetriesPartialWrites",
		"TestWindowsTraceRecordWriterReleasesMutexAfterWriteFailure",
		"TestWindowsTraceSinkCloseClosesRecordMutexOnce",
		"TestWindowsTraceWriterMultiProcessAtomicRecords",
	}, unix: []string{
		"TestRunPickerLaunchesFZFBeforeInitialZoxideCompletes",
		"TestRunPickerParentCancellationStopsLiveZoxideAfterFZFLaunch",
		"TestRunPickerStopsLiveZoxideWhenFZFReturns",
		"TestRunPickerInitialLocalCancellationJoinsStartedZoxide",
		"TestRunPickerInitialPublicationCancellationPrefersParentCause",
		"TestRunPickerWaitsForEnrichmentBeforeClosingServer",
		"TestRunPickerListenFailureJoinsStartedZoxide",
		"TestRunPickerFZFChildExitClosesInputWithoutOverridingResult",
	}},
	{path: "./internal/pathutil", selectedUnix: 3, selectedWindows: 7, tests: []string{}, windows: []string{
		"TestCreateDirectoryTreeRejectsJunctionInBaseAncestry",
		"TestCreateDirectoryTreeRejectsJunctionInQueryComponent",
	}, unix: []string{
		"TestCreateDirectoryTreeRejectsSymlinkInBaseAncestry",
		"TestCreateDirectoryTreeErrorsAndPreservesExistingParents",
	}},
	{path: "./integration", selectedUnix: 47, selectedWindows: 79, tests: []string{
		"TestSecurityGateManifestSelectsEveryRequiredTest",
		"TestSecurityGateRunnerMatchesManifest",
		"TestForgedPayloadCannotAuthorizePreviewOrSelection",
		"TestCancelledNavigationAndPreviewLeakNothing",
		"TestResourceSnapshotFingerprintsArtifactReplacement",
		"TestResourceSnapshotDetectsDescriptorFreeBlockedGoroutine",
		"TestResourceSnapshotDetectsSameSignatureGoroutineReplacement",
		"TestRealFZFPreviewReplacementKillsWholeTree",
		"TestRealFZFPreviewTerminalFailuresKillWholeTree",
		"TestRealFZFAdversarialPromptCannotInjectAction",
		"TestRealFZFPickerNavigationAndNormalMode",
		"TestRealFZFListenSidecarUsesStaticHeaderAndNativeResize",
		"TestRealFZFListenProtocol",
		"TestRealFZFListenSidecarQueryCanaryDoesNotLeak",
		"TestRealFZFListenSidecarProcessTopology",
		"TestRealFZFListenSidecarCDLabelMatchesFZFState",
		"TestRealFZFListenSidecarCDTabSelectionPersistsAcrossFilter",
		"TestRealFZFListenSidecarDoesNotLeakCredentialToPreviewRenderer",
		"TestRealFZFListenSidecarCPLabelTracksQueryFilteringAndAccept",
		"TestRealFZFListenSidecarCPLabelTracksTabSelectionAndAccept",
		"TestRealFZFListenSidecarCDTracksLateZoxideFiltering",
		"TestRealFZFListenSidecarAbortPreservesPickerOutcome",
	}, windows: []string{
		"TestWindowsVerifiedProcessExitsRejectsLiveRecordedIdentity",
		"TestWindowsVerifiedProcessExitsAcceptsAbsentAndReusedPIDs",
		"TestWindowsVerifiedProcessExitsRejectsUnverifiableReusedPID",
		"TestWindowsByteDrainCreatesOverlappedPerRead",
		"TestWindowsTraceServerSignalsReadinessBeforeAnyClient",
		"TestWindowsTraceRecordsSortGloballyByEventTimeAfterReaderDrain",
		"TestWindowsTerminalGracefulTraceCloseDrainsJoinedConnectionsBeforeForceCancel",
		"TestWindowsTracePipeAllowsSubsequentInstancesWithoutFirstInstanceFlag",
		"TestWindowsTraceServerMergesConcurrentConnectionsWhileMainRemainsOpen",
		"TestWindowsTraceServerAcceptsRealCallbacksSequentiallyAndConcurrently",
		"TestWindowsTraceServerCloseCancelsAndClosesAllInstancesOnce",
		"TestWindowsResourceSnapshotUsesExactHandleIdentities",
		"TestWindowsOwnedProcessHandleRegistryReturnsToBaseline",
		"TestWindowsResourceSnapshotFingerprintsDirectoryReplacement",
		"TestWindowsHandleIdentityIncludesObjectForReusedSlot",
		"TestWindowsTask20HandleScopeLifecycleOrdering",
	}, unix: []string{
		"TestPreviewCategoryMatrix",
		"TestParityPreviewResourceProcess",
	}},
}

func TestSecurityGateRunnerMatchesManifest(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "scripts", "security-gate.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(root, "scripts", "security-gate.sh"))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode()&0o111 == 0 {
			t.Fatal("checked-in security gate is not executable")
		}
	}
	assignments := make(map[string]string)
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		for _, key := range []string{"TASK20_PACKAGES", "TASK20_PATTERN"} {
			prefix := key + "='"
			if strings.HasPrefix(line, prefix) && strings.HasSuffix(line, "'") {
				assignments[key] = strings.TrimSuffix(strings.TrimPrefix(line, prefix), "'")
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if assignments["TASK20_PACKAGES"] != task20ManifestPackages {
		t.Fatalf("checked-in gate packages differ from manifest")
	}
	if assignments["TASK20_PATTERN"] != task20FocusedPattern {
		t.Fatalf("checked-in gate pattern differs from manifest")
	}
	if !strings.Contains(string(data), `GOENV=off GOFLAGS= go test "$@" $TASK20_PACKAGES -run "$TASK20_PATTERN"`) {
		t.Fatal("checked-in gate does not sanitize Go configuration while executing the manifest")
	}
	makefile, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(makefile), "GO_TEST_ARGS") {
		t.Fatal("Makefile security gate interpolates GO_TEST_ARGS")
	}
	if !strings.Contains(string(makefile), "./scripts/security-gate.sh -race -count=10 -p=1") {
		t.Fatal("Makefile security gate does not use fixed required arguments")
	}
	if !strings.Contains(string(data), `for argument in "$@"`) || !strings.Contains(string(data), `exit 2`) {
		t.Fatal("checked-in security gate does not validate direct arguments")
	}
}

func TestSecurityGateRejectsUnsafeArgumentsAndForwardsSafeArguments(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX security gate is compile-only on Windows")
	}
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	logPath := filepath.Join(bin, "go-arguments")
	marker := filepath.Join(bin, "injection-ran")
	markerCommand := filepath.Join(bin, "hostile-exec")
	markerScript := fmt.Sprintf("#!/bin/sh\n: > %q\nexec \"$@\"\n", marker)
	if err := os.WriteFile(markerCommand, []byte(markerScript), 0o700); err != nil {
		t.Fatal(err)
	}
	fakeGo := filepath.Join(bin, "go")
	fake := fmt.Sprintf("#!/bin/sh\nprintf 'GOFLAGS=%%s\\nGOENV=%%s\\n' \"${GOFLAGS-<unset>}\" \"${GOENV-<unset>}\" > %q\nprintf '%%s\\n' \"$@\" >> %q\n", logPath, logPath)
	if err := os.WriteFile(fakeGo, []byte(fake), 0o700); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "scripts", "security-gate.sh")
	unsafe := [][]string{{"; id > " + marker}, {"-exec=true"}, {"-args"}, {"./internal/process"}, {"--"}, {"-run=TestAnything"}, {"-timeout=1s;id"}}
	for _, arguments := range unsafe {
		t.Run(arguments[0], func(t *testing.T) {
			_ = os.Remove(logPath)
			command := exec.Command(script, arguments...)
			command.Env = append(os.Environ(), "PATH="+bin)
			if err := command.Run(); err == nil {
				t.Fatalf("unsafe arguments %q succeeded", arguments)
			}
			if _, err := os.Stat(logPath); !os.IsNotExist(err) {
				t.Fatalf("unsafe arguments reached go: %v", err)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("shell metacharacter injection ran: %v", err)
			}
		})
	}

	goEnv := filepath.Join(bin, "hostile-go-env")
	goEnvContents := fmt.Sprintf("GOFLAGS=-exec=%s -run=TestInjected -args\n", markerCommand)
	if err := os.WriteFile(goEnv, []byte(goEnvContents), 0o600); err != nil {
		t.Fatal(err)
	}
	hostileEnvironments := []struct {
		name    string
		entries []string
	}{
		{name: "GOFLAGS exec", entries: []string{"GOFLAGS=-exec=true"}},
		{name: "GOFLAGS run", entries: []string{"GOFLAGS=-run=TestInjected"}},
		{name: "GOFLAGS args", entries: []string{"GOFLAGS=-args"}},
		{name: "GOENV file", entries: []string{"GOFLAGS=", "GOENV=" + goEnv}},
	}
	wantArguments := append([]string{"test", "-race", "-count=10", "-p=1", "-timeout=1m30s"}, strings.Fields(task20ManifestPackages)...)
	wantArguments = append(wantArguments, "-run", task20FocusedPattern)
	for _, hostile := range hostileEnvironments {
		t.Run(hostile.name, func(t *testing.T) {
			_ = os.Remove(logPath)
			command := exec.Command(script, "-race", "-count=10", "-p=1", "-timeout=1m30s")
			command.Env = append(os.Environ(), append([]string{"PATH=" + bin}, hostile.entries...)...)
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("safe arguments failed: %v: %s", err, output)
			}
			data, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			if len(lines) < 2 || lines[0] != "GOFLAGS=" || lines[1] != "GOENV=off" {
				t.Fatalf("hostile Go environment reached invocation: %q", lines)
			}
			if !reflect.DeepEqual(lines[2:], wantArguments) {
				t.Fatalf("safe arguments did not reach go test intact: got %q want %q", lines[2:], wantArguments)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("environment injection ran marker command: %v", err)
			}
		})
	}
}

func TestSecurityGateManifestSelectsEveryRequiredTest(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	pattern := regexp.MustCompile(task20FocusedPattern)
	for _, pkg := range task20GateManifest {
		required := append([]string(nil), pkg.tests...)
		if runtime.GOOS == "windows" {
			required = append(required, pkg.windows...)
		} else {
			required = append(required, pkg.unix...)
		}
		listed := listPackageTests(t, root, pkg.path)
		selected := 0
		for name := range listed {
			if pattern.MatchString(name) {
				selected++
			}
		}
		t.Logf("%s selected=%d", pkg.path, selected)
		expectedSelected := pkg.selectedUnix
		if runtime.GOOS == "windows" {
			expectedSelected = pkg.selectedWindows
		}
		if selected != expectedSelected {
			t.Errorf("%s: documented focused regex selected=%d want=%d", pkg.path, selected, expectedSelected)
		}
		for _, name := range required {
			if _, ok := listed[name]; !ok {
				t.Errorf("%s: required test %s is absent", pkg.path, name)
			}
			if !pattern.MatchString(name) {
				t.Errorf("%s: documented focused regex omits %s", pkg.path, name)
			}
		}
	}
}

func listPackageTests(t *testing.T, root, pkg string) map[string]struct{} {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "test", "-list", "^Test", pkg)
	command.Dir = root
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("go test -list %s: %v", pkg, err)
	}
	tests := make(map[string]struct{})
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		name := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(name, "Test") {
			tests[name] = struct{}{}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if len(tests) == 0 {
		t.Fatalf("go test -list %s returned no tests", pkg)
	}
	return tests
}
