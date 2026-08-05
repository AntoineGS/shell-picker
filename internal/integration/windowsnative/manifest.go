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
	{"./internal/app", `^(TestClassifyWindowsTracePath|TestOpenWindowsTraceSinkUsesPipeAndFileDispositions|TestOpenWindowsTraceSinkValidatesBeforeTruncatingAndClosesOnFailure|TestWindowsTraceWriterMultiProcessAtomicRecords)$`},
	{"./internal/pathutil", `^(TestWindowsParentModel|TestWindowsRelativeAndValidation|TestCompactHomeWindows|TestPromptDisplayHomeWindows|TestWindowsUNCNavigationPure|TestWindowsUNCMixedAndRepeatedSeparatorsPure|TestListDrivesAscending|TestAbsoluteAncestryWindowsDrive|TestAbsoluteAncestryWindowsUNC|TestCreateDirectoryTreeRejectsJunctionInBaseAncestry|TestCreateDirectoryTreeRejectsJunctionInQueryComponent|TestCreateDirectoryTreeWindowsRollbackAndExistingFile)$`},
	{"./integration", `^(TestWindowsResourceSnapshotUsesExactHandleIdentities|TestWindowsOwnedProcessHandleRegistryReturnsToBaseline|TestWindowsResourceSnapshotFingerprintsDirectoryReplacement|TestWindowsHandleIdentityIncludesObjectForReusedSlot|TestWindowsHandleSnapshotBufferGrowth|TestWindowsHandleSnapshotCanGrowPastFormerRetryCount|TestWindowsHandleSnapshotGrowthRemainsGeometricWhenNeededKeepsGrowing|TestWindowsHandleIdentityDifferenceListsAddedAndRemoved|TestWindowsTask20HandleScopeLifecycleOrdering|TestWindowsTask20HandleScopeWaitsForTransientClosure|TestWindowsTask20HandleScopeReportsPersistentIdentity|TestWindowsTask20HandleScopeRetainsEvidenceOnQueryError|TestWindowsTask20HandleScopeTreatsReusedSlotAsClosed|TestTask20ObjectTypeQueryGrowsGeometrically|TestTask20ObjectTypeQueryRejectsOversizedResponse|TestTask20KnownObjectTypePolicy|TestTask20ClassifyHandleRecognizesNativeObjectTypes|TestTask20UnknownObjectTypeFailsClosed|TestWindowsTask20ScopePolicyIsTypeExact|TestWindowsApplicationHandleDifferenceIncludesType|TestWindowsRawHandleCountDoesNotChangeApplicationDifference|TestWindowsResourceGateReportsPersistentApplicationIdentityAfterFiltering|TestWindowsResourceGateReportsPersistentETWIdentityAfterFiltering|TestParityWindowsSemanticSubstitutions|TestParityWindowsUnicodeSpaceControlDisplayReplacement|TestPlatformPrerequisites)$`},
}

func approvedManifest() []Package {
	return []Package{
		{"./internal/session", `^(TestWindowsRootParentAndHomeTransitions|TestValidateCPWindowsCrossVolumeUsesAbsolutePath)$`},
		{"./internal/sessionipc", `^(TestRoutesMapResponsesAndErrors|TestPreviewValidationAndTelemetry|TestFinishedTelemetryExactChildBounds|TestServerCloseCancelsCooperativeBackend|TestServerRejectsSeventeenthHandlerAndCloseCancelsAndJoins)$`},
		{"./internal/candidate", `^(TestWindowsDriveAndUNCRootsUseCanonicalVirtualDrivesParent|TestWindowsNonRootParentRemainsFilesystemRecord|TestEnumerateDrivesOrderKindAndIdentity|TestEnumerateDrivesErrorPublishesNothing)$`},
		{"./internal/process", `^(TestPreparedStreamsPumpCount|TestWindowsStartFailureStagesCloseEverything|TestCleanupFailuresDoNotMaskStartFailure|TestWindowsCancelTerminatesJob|TestWindowsForegroundAndInheritedJobsTerminateDescendants|TestCreateProcessPassesStreamsAndArguments|TestWindowsRejectsExtraFilesBeforeProcessAttempt|TestWindowsForegroundUsesOwnedJobWithoutTTY|TestRetainedInheritedJobKillsDescendantAfterChildWait|TestWindowsExitErrorPreservesWaitDelayClassification|TestCreateProcessInheritsOnlyExplicitChildHandles|TestSharedStdoutStderrWriterIsSerialized|TestSanitizeEnvWindowsCaseInsensitiveAndLastWins|TestSanitizeEnvWindowsDeduplicatesControlledKeys)$`},
		{"./internal/callback", `^(TestSetCursorWritesWindowsConsoleWithoutStdout)$`},
		{"./internal/preview", `^(TestWindowsCachePutRootSwapIsRejectedOrExplicitlyDenied|TestWindowsCacheRejectsSymlinkRootAndEntry|TestWindowsArtifactCleanupRetriesAfterSharingViolation|TestWindowsStaleCleanupLeavesUnvalidatedPrivateDirectory|TestWindowsStageCreationValidationFailureLeavesNoPrivateArtifact|TestWindowsStaleCleanupRejectsAttackerStageLookalikes|TestWindowsStaleCleanupRemovesGenuineAbandonedStage|TestWindowsStageMarkerValidationFailureLeavesNoPrivateStage|TestExternalRendererSpecRequestsWindowsNestedJob|TestStagedArtifactRejectsReplacementAfterExclusiveCreation|TestStagedArtifactTruncatesValidatedCreationIdentity|TestRendererReadsValidatedStageWhenPathIsReplaced)$`},
		{"./internal/app", `^(TestClassifyWindowsTracePath|TestOpenWindowsTraceSinkUsesPipeAndFileDispositions|TestOpenWindowsTraceSinkValidatesBeforeTruncatingAndClosesOnFailure|TestWindowsTraceWriterMultiProcessAtomicRecords)$`},
		{"./internal/pathutil", `^(TestWindowsParentModel|TestWindowsRelativeAndValidation|TestCompactHomeWindows|TestPromptDisplayHomeWindows|TestWindowsUNCNavigationPure|TestWindowsUNCMixedAndRepeatedSeparatorsPure|TestListDrivesAscending|TestAbsoluteAncestryWindowsDrive|TestAbsoluteAncestryWindowsUNC|TestCreateDirectoryTreeRejectsJunctionInBaseAncestry|TestCreateDirectoryTreeRejectsJunctionInQueryComponent|TestCreateDirectoryTreeWindowsRollbackAndExistingFile)$`},
		{"./integration", `^(TestWindowsResourceSnapshotUsesExactHandleIdentities|TestWindowsOwnedProcessHandleRegistryReturnsToBaseline|TestWindowsResourceSnapshotFingerprintsDirectoryReplacement|TestWindowsHandleIdentityIncludesObjectForReusedSlot|TestWindowsHandleSnapshotBufferGrowth|TestWindowsHandleSnapshotCanGrowPastFormerRetryCount|TestWindowsHandleSnapshotGrowthRemainsGeometricWhenNeededKeepsGrowing|TestWindowsHandleIdentityDifferenceListsAddedAndRemoved|TestWindowsTask20HandleScopeLifecycleOrdering|TestWindowsTask20HandleScopeWaitsForTransientClosure|TestWindowsTask20HandleScopeReportsPersistentIdentity|TestWindowsTask20HandleScopeRetainsEvidenceOnQueryError|TestWindowsTask20HandleScopeTreatsReusedSlotAsClosed|TestTask20ObjectTypeQueryGrowsGeometrically|TestTask20ObjectTypeQueryRejectsOversizedResponse|TestTask20KnownObjectTypePolicy|TestTask20ClassifyHandleRecognizesNativeObjectTypes|TestTask20UnknownObjectTypeFailsClosed|TestWindowsTask20ScopePolicyIsTypeExact|TestWindowsApplicationHandleDifferenceIncludesType|TestWindowsRawHandleCountDoesNotChangeApplicationDifference|TestWindowsResourceGateReportsPersistentApplicationIdentityAfterFiltering|TestWindowsResourceGateReportsPersistentETWIdentityAfterFiltering|TestParityWindowsSemanticSubstitutions|TestParityWindowsUnicodeSpaceControlDisplayReplacement|TestPlatformPrerequisites)$`},
	}
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
	authority := approvedManifest()
	approvedByPath := make(map[string]Package, len(authority))
	for _, expected := range authority {
		approvedByPath[expected.Path] = expected
	}

	for _, pkg := range Packages {
		if strings.TrimSpace(pkg.Path) == "" {
			return fmt.Errorf("package path is empty")
		}
		if _, exists := seen[pkg.Path]; exists {
			return fmt.Errorf("duplicate package path %q", pkg.Path)
		}
		seen[pkg.Path] = struct{}{}

		expected, ok := approvedByPath[pkg.Path]
		if !ok {
			return fmt.Errorf("package path %q is not approved", pkg.Path)
		}

		names, err := parsePattern(pkg.Pattern)
		if err != nil {
			return fmt.Errorf("package %s pattern: %w", pkg.Path, err)
		}
		approvedNames, err := parsePattern(expected.Pattern)
		if err != nil {
			return fmt.Errorf("approved package %s pattern: %w", expected.Path, err)
		}
		approvedNameSet := make(map[string]struct{}, len(approvedNames))
		for _, name := range approvedNames {
			approvedNameSet[name] = struct{}{}
		}
		for _, name := range names {
			if _, ok := approvedNameSet[name]; !ok {
				return fmt.Errorf("package %s pattern contains unapproved test %q", pkg.Path, name)
			}
		}
		for _, authority := range unixAuthorities {
			if strings.Contains(pkg.Pattern, authority) {
				return fmt.Errorf("package %s pattern contains Unix authority %q", pkg.Path, authority)
			}
		}
	}

	if len(Packages) != len(authority) {
		return fmt.Errorf("manifest has %d packages, want %d", len(Packages), len(authority))
	}
	for i, pkg := range Packages {
		expected := authority[i]
		if pkg.Path != expected.Path {
			return fmt.Errorf("package %d path %q is out of deterministic order", i, pkg.Path)
		}
		if pkg.Pattern != expected.Pattern {
			return fmt.Errorf("package %s pattern does not match the approved manifest", pkg.Path)
		}
	}
	return nil
}

var canonicalTestName = regexp.MustCompile(`^Test[A-Z][A-Za-z0-9_]*$`)

func parsePattern(pattern string) ([]string, error) {
	const (
		prefix = "^("
		suffix = ")$"
	)
	if !strings.HasPrefix(pattern, prefix) || !strings.HasSuffix(pattern, suffix) {
		return nil, fmt.Errorf("must be exactly anchored as ^(names)$: %q", pattern)
	}

	body := strings.TrimSuffix(strings.TrimPrefix(pattern, prefix), suffix)
	if body == "" {
		return nil, fmt.Errorf("must contain at least one test name")
	}
	names := strings.Split(body, "|")
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		if !canonicalTestName.MatchString(name) {
			return nil, fmt.Errorf("contains non-literal test name %q", name)
		}
		if _, exists := seen[name]; exists {
			return nil, fmt.Errorf("contains duplicate test name %q", name)
		}
		seen[name] = struct{}{}
	}
	if _, err := regexp.Compile(pattern); err != nil {
		return nil, fmt.Errorf("is not a valid regular expression: %w", err)
	}
	return names, nil
}
