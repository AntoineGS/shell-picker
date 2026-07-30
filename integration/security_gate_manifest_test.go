package integration

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

const task20FocusedPattern = `^Test(Actor|Handle|Reduce|NormalEscape|Validate|Server|Client|FinishedTelemetry|CachedPolicy|Fresh|IndependentFresh|CPNever|CallerCancellation|BuilderZoxide|Zoxide|EnumerateReadDir|Kqueue|RejectsNonIdentifiable|WaitDelay|WaitIs|CancellationCloses|ExitErrorPrecedesBlocking|OrdinaryCompletion|Foreground|RestoreForeground|RetainedInherited|CancelKills|InheritedTree|Preview|Archive|Zip|Cache|Converter|Renderer|ExternalRenderer|Terminal|SessionSpec|Run|ParseOutput|ActionArgument|TokenCanary|PickerBackend|CreateDirectory|SecurityGate|Forged|CancelledNavigation|ResourceSnapshot|ParityPreview|RealFZF|Windows|CreateProcess|RunnerBeforeStart|RunnerNilBeforeStart)`

const task20ManifestPackages = `./internal/session ./internal/sessionipc ./internal/candidate ./internal/process ./internal/callback ./internal/preview ./internal/fzf ./internal/app ./internal/pathutil ./integration`

type task20GatePackage struct {
	path                          string
	tests                         []string
	windows                       []string
	unix                          []string
	selectedUnix, selectedWindows int
}

var task20GateManifest = []task20GatePackage{
	{path: "./internal/session", selectedUnix: 30, selectedWindows: 32, tests: []string{
		"TestActorKeepsReadsLiveAndPublishesCompleteProposalAtomically",
		"TestActorCreatedTreeDiscardOrdering",
		"TestHandleAddCreateErrorsRetainAddSnapshotAndDoNotBuild",
		"TestHandleAddGenerationFailureRollsBackAndPreservesParents",
		"TestHandleAddRollbackWaitsForGenerationCompletion",
		"TestHandleTransfersActualCreatedTreeForStaleBaseRollback",
		"TestHandleAddPublishesCompleteStateAtomicallyAfterSingleGeneration",
		"TestReduceValidAddIsPureAndClonesIntent",
		"TestNormalEscapeHasOnlyClearMultiEffect",
		"TestValidateCDRequiresOneExactFilesystemDirectoryRecord",
		"TestValidateCPRejectsMalformedUnknownResidualAndVirtual",
	}},
	{path: "./internal/sessionipc", selectedUnix: 16, selectedWindows: 16, tests: []string{
		"TestServerRejectsNoncanonicalRouteTargetsBeforeBackend",
		"TestServerAcceptsOnlyOneExactRawAuthorizationValue",
		"TestServerRejectsChunkedRequestAt64KiBPlusOne",
		"TestServerRejectsSeventeenthHandlerAndCloseCancelsAndJoins",
		"TestServerCloseCancelsCooperativeBackendAndJoinsHandler",
		"TestClientBoundsEveryResponseClassAtLimitAndLimitPlusOne",
		"TestClientClosesOverlimitBodyWithoutReusableTransport",
		"TestServerRejectsBackendReturnedVirtualPreview",
		"TestFinishedTelemetryExactChildBounds",
	}},
	{path: "./internal/candidate", selectedUnix: 17, selectedWindows: 18, tests: []string{
		"TestCachedPolicyAttemptsOnceForSessionAndLaterReportsCached",
		"TestFreshZeroTimeoutIsAuthoritativeUnlimitedPerGeneration",
		"TestFreshBuilderSerializesSessionQueriesAndCancelledWaiterDoesNotAttempt",
		"TestIndependentFreshSessionBuildersMayQueryConcurrently",
		"TestCPNeverLoadsCacheOrInvokesFreshFactory",
		"TestCallerCancellationBeforePrivateTimeoutWinsAndReaps",
		"TestBuilderZoxideTimeoutIsSoftAndDiscardsPartialOutput",
		"TestZoxideMalformedAndProcessFailuresDiscardCompleteBuffer",
		"TestZoxideMissingAndSpawnFailureAttemptWithoutStart",
		"TestEnumerateReadDirErrorPublishesNothing",
	}},
	{path: "./internal/process", selectedUnix: 23, selectedWindows: 24, tests: []string{
		"TestKqueueEventValidation",
		"TestKqueueRegistrationValidatedBeforeWait",
		"TestRejectsNonIdentifiableValueCloserBeforeAttempt",
		"TestWaitDelayClosesBlockingPumpedStreams",
		"TestCancellationClosesBlockingPumpedStream",
		"TestExitErrorPrecedesBlockingPumpWaitDelay",
		"TestOrdinaryCompletionDoesNotClosePumpedCloser",
		"TestWaitDelayClosesSharedPointerOnce",
		"TestWaitIsSingleUseAndWaitDelayBoundsInheritedPipe",
		"TestForegroundTreeOwnsTTYAndRestoresPreviousGroup",
		"TestRestoreForegroundPGRRestoresMaskOnIoctlError",
		"TestRetainedInheritedTreeKillsGroupAfterChildWait",
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
	}},
	{path: "./internal/callback", selectedUnix: 6, selectedWindows: 6, tests: []string{
		"TestPreviewRejectsVirtualAndRelativeBeforeRenderer",
		"TestPreviewTerminalResourceSkipsFinishedTelemetry",
		"TestPreviewAggregatesSequentialChildTelemetry",
	}},
	{path: "./internal/preview", selectedUnix: 32, selectedWindows: 36, tests: []string{
		"TestArchiveLimitsEntriesBytesAndDeadline",
		"TestZipPreflightRejectsCentralDirectoryBombBeforeArchiveOpen",
		"TestCachePutAnchorsRootAcrossSwap",
		"TestCacheConcurrentProductionPutHasImmutableSingleLinkWinner",
		"TestConverterFinalValidationRejectsOversizedArtifact",
		"TestRendererReadsValidatedStageWhenPathIsReplaced",
		"TestExternalRendererOutputLimitKillsInheritedGroupWithoutFallback",
		"TestExternalRendererDeadlineKillsInheritedGroupWithoutFallback",
		"TestExternalRendererWaitDelayKillsInheritedGroupWithoutFallback",
		"TestRendererFailuresAreWaitedSequentiallyBeforeNativeFallback",
		"TestTerminalRetriesBlockedCleanupAfterTreeKill",
	}},
	{path: "./internal/fzf", selectedUnix: 12, selectedWindows: 12, tests: []string{
		"TestSessionSpecSeparatesCallbackCredentialsFromInheritedEnvironment",
		"TestRunRejectsUnsafeConfigurationBeforeProcessStart",
		"TestRunDoesNotProbeVersion",
		"TestParseOutputRejectsMalformedFrames",
		"TestActionArgumentDelimiterCorpusCannotInjectAction",
	}},
	{path: "./internal/app", selectedUnix: 19, selectedWindows: 17, tests: []string{
		"TestTokenCanaryUsesActualCallbackCredentialAndExcludesNamedSinks",
		"TestPickerBackendRejectsAuthorizedVirtualBeforeFilesystemAndOutput",
		"TestRunPickerClosesCallbackEndpointBeforeReturning",
		"TestRunPickerAppliesZoxidePolicyProcessBudgets",
		"TestRunPickerParentCancellationStopsActiveCallbackGenerationBeforeFZFReturns",
	}},
	{path: "./internal/pathutil", selectedUnix: 3, selectedWindows: 7, tests: []string{
		"TestCreateDirectoryTreeRejectsSymlinkInBaseAncestry",
		"TestCreateDirectoryTreeErrorsAndPreservesExistingParents",
	}, windows: []string{
		"TestCreateDirectoryTreeRejectsJunctionInBaseAncestry",
		"TestCreateDirectoryTreeRejectsJunctionInQueryComponent",
	}},
	{path: "./integration", selectedUnix: 23, selectedWindows: 21, tests: []string{
		"TestSecurityGateManifestSelectsEveryRequiredTest",
		"TestSecurityGateRunnerMatchesManifest",
		"TestForgedPayloadCannotAuthorizePreviewOrSelection",
		"TestCancelledNavigationAndPreviewLeakNothing",
		"TestResourceSnapshotFingerprintsArtifactReplacement",
		"TestResourceSnapshotDetectsDescriptorFreeBlockedGoroutine",
		"TestPreviewCategoryMatrix",
		"TestParityPreviewResourceProcess",
		"TestRealFZFPreviewReplacementKillsWholeTree",
		"TestRealFZFPreviewTerminalFailuresKillWholeTree",
		"TestRealFZFAdversarialPromptCannotInjectAction",
	}, windows: []string{
		"TestWindowsResourceSnapshotUsesExactHandleIdentities",
		"TestWindowsOwnedProcessHandleRegistryReturnsToBaseline",
		"TestWindowsResourceSnapshotFingerprintsDirectoryReplacement",
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
	if !strings.Contains(string(data), `go test "$@" $TASK20_PACKAGES -run "$TASK20_PATTERN"`) {
		t.Fatal("checked-in gate does not execute the manifest with forwarded go test arguments")
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
