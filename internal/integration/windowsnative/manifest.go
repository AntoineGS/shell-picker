package windowsnative

import (
	"fmt"
	"regexp"
	"strings"
)

type Package struct {
	Path    string
	Pattern string
}

var Packages = []Package{
	{"./internal/session", `^(TestWindowsRootParentAndHomeTransitions|TestValidateCPWindowsCrossVolumeUsesAbsolutePath)$`},
	{"./internal/sessionipc", `^(TestRoutesMapResponsesAndErrors|TestPreviewValidationAndTelemetry|TestFinishedTelemetryExactChildBounds|TestServerCloseCancelsCooperativeBackend|TestServerRejectsSeventeenthHandlerAndCloseCancelsAndJoins)$`},
	{"./internal/candidate", `^(TestWindowsDriveAndUNCRootsUseCanonicalVirtualDrivesParent|TestWindowsNonRootParentRemainsFilesystemRecord|TestEnumerateDrivesOrderKindAndIdentity|TestEnumerateDrivesErrorPublishesNothing)$`},
	{"./internal/process", `^(TestPreparedStreamsPumpCount|TestWindowsStartFailureStagesCloseEverything|TestCleanupFailuresDoNotMaskStartFailure|TestWindowsCancelTerminatesJob|TestWindowsForegroundAndInheritedJobsTerminateDescendants|TestCreateProcessPassesStreamsAndArguments|TestWindowsRejectsExtraFilesBeforeProcessAttempt|TestWindowsForegroundUsesOwnedJobWithoutTTY|TestRetainedInheritedJobKillsDescendantAfterChildWait|TestWindowsExitErrorPreservesWaitDelayClassification|TestCreateProcessInheritsOnlyExplicitChildHandles|TestSharedStdoutStderrWriterIsSerialized|TestSanitizeEnvWindowsCaseInsensitiveAndLastWins|TestSanitizeEnvWindowsDeduplicatesControlledKeys)$`},
	{"./internal/callback", `^(TestSetCursorWritesWindowsConsoleWithoutStdout)$`},
	{"./internal/preview", `^(TestWindowsCachePutRootSwapIsRejectedOrExplicitlyDenied|TestWindowsCacheRejectsSymlinkRootAndEntry|TestWindowsArtifactCleanupRetriesAfterSharingViolation|TestWindowsStaleCleanupLeavesUnvalidatedPrivateDirectory|TestWindowsStageCreationValidationFailureLeavesNoPrivateArtifact|TestWindowsStaleCleanupRejectsAttackerStageLookalikes|TestWindowsStaleCleanupRemovesGenuineAbandonedStage|TestWindowsStageMarkerValidationFailureLeavesNoPrivateStage|TestExternalRendererSpecRequestsWindowsNestedJob|TestStagedArtifactRejectsReplacementAfterExclusiveCreation|TestStagedArtifactTruncatesValidatedCreationIdentity|TestRendererReadsValidatedStageWhenPathIsReplaced)$`},
	{"./internal/app", `^(TestClassifyWindowsTracePath|TestOpenWindowsTraceSinkUsesPipeAndFileDispositions|TestOpenWindowsTraceSinkValidatesBeforeTruncatingAndClosesOnFailure)$`},
	{"./internal/pathutil", `^(TestWindowsParentModel|TestWindowsRelativeAndValidation|TestCompactHomeWindows|TestPromptDisplayHomeWindows|TestWindowsUNCNavigationPure|TestWindowsUNCMixedAndRepeatedSeparatorsPure|TestListDrivesAscending|TestAbsoluteAncestryWindowsDrive|TestAbsoluteAncestryWindowsUNC|TestCreateDirectoryTreeRejectsJunctionInBaseAncestry|TestCreateDirectoryTreeRejectsJunctionInQueryComponent|TestCreateDirectoryTreeWindowsRollbackAndExistingFile)$`},
	{"./integration", `^(TestWindowsResourceSnapshotUsesExactHandleIdentities|TestWindowsOwnedProcessHandleRegistryReturnsToBaseline|TestWindowsResourceSnapshotFingerprintsDirectoryReplacement|TestWindowsHandleIdentityIncludesObjectForReusedSlot|TestWindowsHandleSnapshotBufferGrowth|TestWindowsHandleSnapshotCanGrowPastFormerRetryCount|TestWindowsHandleSnapshotGrowthRemainsGeometricWhenNeededKeepsGrowing|TestWindowsHandleIdentityDifferenceListsAddedAndRemoved|TestWindowsTask20HandleScopeLifecycleOrdering|TestWindowsTask20HandleScopeWaitsForTransientClosure|TestWindowsTask20HandleScopeReportsPersistentIdentity|TestWindowsTask20HandleScopeRetainsEvidenceOnQueryError|TestWindowsTask20HandleScopeTreatsReusedSlotAsClosed|TestTask20ObjectTypeQueryGrowsGeometrically|TestTask20ObjectTypeQueryRejectsOversizedResponse|TestTask20KnownObjectTypePolicy|TestTask20UnknownObjectTypeFailsClosed|TestWindowsApplicationHandleDifferenceIncludesType|TestWindowsRuntimeHandlesDoNotEnterApplicationSnapshot|TestWindowsResourceGateRejectsPersistentApplicationIdentity|TestParityWindowsSemanticSubstitutions|TestParityWindowsUnicodeSpaceControlDisplayReplacement|TestPlatformPrerequisites)$`},
}

var unixAuthorities = []string{
	"ForegroundTreeOwnsTTY",
	"Kqueue",
	"SymlinkInBaseAncestry",
	"ParityPreviewResourceProcess",
}

func Validate() error {
	if len(Packages) == 0 {
		return fmt.Errorf("manifest is empty")
	}

	seen := make(map[string]struct{}, len(Packages))
	for _, pkg := range Packages {
		if strings.TrimSpace(pkg.Path) == "" {
			return fmt.Errorf("package path is empty")
		}
		if _, exists := seen[pkg.Path]; exists {
			return fmt.Errorf("duplicate package path %q", pkg.Path)
		}
		seen[pkg.Path] = struct{}{}

		if !strings.HasPrefix(pkg.Pattern, "^(") || !strings.HasSuffix(pkg.Pattern, ")$") {
			return fmt.Errorf("package %s pattern is not exactly anchored: %q", pkg.Path, pkg.Pattern)
		}
		if _, err := regexp.Compile(pkg.Pattern); err != nil {
			return fmt.Errorf("package %s pattern: %w", pkg.Path, err)
		}
		for _, authority := range unixAuthorities {
			if strings.Contains(pkg.Pattern, authority) {
				return fmt.Errorf("package %s pattern contains Unix authority %q", pkg.Path, authority)
			}
		}
	}
	return nil
}
