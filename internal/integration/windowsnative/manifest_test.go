package windowsnative

import (
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestWindowsNativeManifestHasUniquePackagesAndAnchoredPatterns(t *testing.T) {
	seen := make(map[string]struct{})
	for _, pkg := range Packages {
		if _, exists := seen[pkg.Path]; exists {
			t.Errorf("duplicate package %s", pkg.Path)
		}
		seen[pkg.Path] = struct{}{}
		if !strings.HasPrefix(pkg.Pattern, "^(") || !strings.HasSuffix(pkg.Pattern, ")$") {
			t.Errorf("package %s pattern is not exactly anchored: %q", pkg.Path, pkg.Pattern)
		}
		if _, err := regexp.Compile(pkg.Pattern); err != nil {
			t.Errorf("package %s pattern: %v", pkg.Path, err)
		}
	}
}

func TestWindowsNativeManifestRejectsUnixAuthorities(t *testing.T) {
	for _, rejected := range []string{"ForegroundTreeOwnsTTY", "Kqueue", "SymlinkInBaseAncestry", "ParityPreviewResourceProcess"} {
		for _, pkg := range Packages {
			if strings.Contains(pkg.Pattern, rejected) {
				t.Errorf("Windows manifest contains Unix authority %s", rejected)
			}
		}
	}
}

func TestWindowsNativeManifestContainsExactCurrentAuthorities(t *testing.T) {
	want := []struct {
		path  string
		names []string
	}{
		{
			path: "./internal/session",
			names: []string{
				"TestWindowsRootParentAndHomeTransitions",
				"TestValidateCPWindowsCrossVolumeUsesAbsolutePath",
			},
		},
		{
			path: "./internal/sessionipc",
			names: []string{
				"TestRoutesMapResponsesAndErrors",
				"TestPreviewValidationAndTelemetry",
				"TestFinishedTelemetryExactChildBounds",
				"TestServerCloseCancelsCooperativeBackend",
				"TestServerRejectsSeventeenthHandlerAndCloseCancelsAndJoins",
			},
		},
		{
			path: "./internal/candidate",
			names: []string{
				"TestWindowsDriveAndUNCRootsUseCanonicalVirtualDrivesParent",
				"TestWindowsNonRootParentRemainsFilesystemRecord",
				"TestEnumerateDrivesOrderKindAndIdentity",
				"TestEnumerateDrivesErrorPublishesNothing",
			},
		},
		{
			path: "./internal/process",
			names: []string{
				"TestPreparedStreamsPumpCount",
				"TestWindowsStartFailureStagesCloseEverything",
				"TestCleanupFailuresDoNotMaskStartFailure",
				"TestWindowsCancelTerminatesJob",
				"TestWindowsForegroundAndInheritedJobsTerminateDescendants",
				"TestCreateProcessPassesStreamsAndArguments",
				"TestWindowsRejectsExtraFilesBeforeProcessAttempt",
				"TestWindowsForegroundUsesOwnedJobWithoutTTY",
				"TestRetainedInheritedJobKillsDescendantAfterChildWait",
				"TestWindowsExitErrorPreservesWaitDelayClassification",
				"TestCreateProcessInheritsOnlyExplicitChildHandles",
				"TestSharedStdoutStderrWriterIsSerialized",
				"TestSanitizeEnvWindowsCaseInsensitiveAndLastWins",
				"TestSanitizeEnvWindowsDeduplicatesControlledKeys",
			},
		},
		{
			path:  "./internal/callback",
			names: []string{"TestSetCursorWritesWindowsConsoleWithoutStdout"},
		},
		{
			path: "./internal/preview",
			names: []string{
				"TestWindowsCachePutRootSwapIsRejectedOrExplicitlyDenied",
				"TestWindowsCacheRejectsSymlinkRootAndEntry",
				"TestWindowsArtifactCleanupRetriesAfterSharingViolation",
				"TestWindowsStaleCleanupLeavesUnvalidatedPrivateDirectory",
				"TestWindowsStageCreationValidationFailureLeavesNoPrivateArtifact",
				"TestWindowsStaleCleanupRejectsAttackerStageLookalikes",
				"TestWindowsStaleCleanupRemovesGenuineAbandonedStage",
				"TestWindowsStageMarkerValidationFailureLeavesNoPrivateStage",
				"TestExternalRendererSpecRequestsWindowsNestedJob",
				"TestStagedArtifactRejectsReplacementAfterExclusiveCreation",
				"TestStagedArtifactTruncatesValidatedCreationIdentity",
				"TestRendererReadsValidatedStageWhenPathIsReplaced",
			},
		},
		{
			path: "./internal/app",
			names: []string{
				"TestClassifyWindowsTracePath",
				"TestOpenWindowsTraceSinkUsesPipeAndFileDispositions",
				"TestOpenWindowsTraceSinkValidatesBeforeTruncatingAndClosesOnFailure",
				"TestWindowsTraceWriterMultiProcessAtomicRecords",
			},
		},
		{
			path: "./internal/pathutil",
			names: []string{
				"TestWindowsParentModel",
				"TestWindowsRelativeAndValidation",
				"TestCompactHomeWindows",
				"TestPromptDisplayHomeWindows",
				"TestWindowsUNCNavigationPure",
				"TestWindowsUNCMixedAndRepeatedSeparatorsPure",
				"TestListDrivesAscending",
				"TestAbsoluteAncestryWindowsDrive",
				"TestAbsoluteAncestryWindowsUNC",
				"TestCreateDirectoryTreeRejectsJunctionInBaseAncestry",
				"TestCreateDirectoryTreeRejectsJunctionInQueryComponent",
				"TestCreateDirectoryTreeWindowsRollbackAndExistingFile",
			},
		},
		{
			path: "./integration",
			names: []string{
				"TestWindowsResourceSnapshotUsesExactHandleIdentities",
				"TestWindowsOwnedProcessHandleRegistryReturnsToBaseline",
				"TestWindowsResourceSnapshotFingerprintsDirectoryReplacement",
				"TestWindowsHandleIdentityIncludesObjectForReusedSlot",
				"TestWindowsHandleSnapshotBufferGrowth",
				"TestWindowsHandleSnapshotCanGrowPastFormerRetryCount",
				"TestWindowsHandleSnapshotGrowthRemainsGeometricWhenNeededKeepsGrowing",
				"TestWindowsHandleIdentityDifferenceListsAddedAndRemoved",
				"TestWindowsTask20HandleScopeLifecycleOrdering",
				"TestWindowsTask20HandleScopeWaitsForTransientClosure",
				"TestWindowsTask20HandleScopeReportsPersistentIdentity",
				"TestWindowsTask20HandleScopeRetainsEvidenceOnQueryError",
				"TestWindowsTask20HandleScopeTreatsReusedSlotAsClosed",
				"TestTask20ObjectTypeQueryGrowsGeometrically",
				"TestTask20ObjectTypeQueryRejectsOversizedResponse",
				"TestTask20KnownObjectTypePolicy",
				"TestTask20ClassifyHandleRecognizesNativeObjectTypes",
				"TestTask20UnknownObjectTypeFailsClosed",
				"TestWindowsTask20ScopePolicyIsTypeExact",
				"TestWindowsApplicationHandleDifferenceIncludesType",
				"TestWindowsRawHandleCountDoesNotChangeApplicationDifference",
				"TestWindowsResourceGateReportsPersistentApplicationIdentityAfterFiltering",
				"TestWindowsResourceGateReportsPersistentETWIdentityAfterFiltering",
				"TestParityWindowsSemanticSubstitutions",
				"TestParityWindowsUnicodeSpaceControlDisplayReplacement",
				"TestPlatformPrerequisites",
			},
		},
	}

	if len(Packages) != len(want) {
		t.Fatalf("manifest has %d packages, want %d", len(Packages), len(want))
	}
	for i, expected := range want {
		got := Packages[i]
		if got.Path != expected.path {
			t.Errorf("package %d path=%q want %q", i, got.Path, expected.path)
		}
		wantPattern := "^(" + strings.Join(expected.names, "|") + ")$"
		if got.Pattern != wantPattern {
			t.Errorf("package %s pattern=%q want=%q", got.Path, got.Pattern, wantPattern)
		}
	}
}

func TestWindowsNativeManifestAlternativesAreDeclaredTests(t *testing.T) {
	root := moduleRoot(t)
	for _, pkg := range Packages {
		packageDir := filepath.Join(root, filepath.FromSlash(pkg.Path))
		declared := declaredTestNames(t, packageDir)
		for _, name := range patternAlternatives(pkg.Pattern) {
			if !declared[name] {
				t.Errorf("package %s manifest selects undeclared test %s", pkg.Path, name)
			}
		}
	}
}

func TestWindowsNativeApprovedManifestAuthorityIsIndependent(t *testing.T) {
	approved := approvedManifest()
	if len(approved) == 0 {
		t.Fatal("approved manifest is empty")
	}

	original := append([]Package(nil), Packages...)
	t.Cleanup(func() { Packages = original })
	Packages[0] = Package{
		Path:    "./...",
		Pattern: `^(TestWindowsRootParentAndHomeTransitions)$`,
	}
	if err := Validate(); err == nil {
		t.Fatal("Validate accepted a source-edited unsafe package")
	}
	if approved[0].Path != "./internal/session" {
		t.Fatalf("approved authority changed with candidate: %q", approved[0].Path)
	}
}

func TestWindowsNativeDeclarationSetExcludesUnixOnlyAndInvalidSignatures(t *testing.T) {
	packageDir := t.TempDir()
	writeFixtureFile(t, packageDir, "fixture_windows_test.go", `package fixture

import "testing"

func TestWindowsOnly(t *testing.T) {}
`)
	writeFixtureFile(t, packageDir, "fixture_unix_test.go", `//go:build !windows

package fixture

import "testing"

func TestUnixOnly(t *testing.T) {}
`)
	writeFixtureFile(t, packageDir, "fixture_windows_invalid_test.go", `//go:build windows

package fixture

import "testing"

func TestNoParameter() {}
func TestResult(t *testing.T) bool { return true }
func TestGeneric[T any](t *testing.T) {}

type fixture struct{}

func (fixture) TestReceiver(t *testing.T) {}
`)

	declared := declaredTestNames(t, packageDir)
	if !declared["TestWindowsOnly"] {
		t.Fatal("Windows declaration set omitted valid test")
	}
	for _, rejected := range []string{
		"TestUnixOnly",
		"TestNoParameter",
		"TestResult",
		"TestGeneric",
		"TestReceiver",
	} {
		if declared[rejected] {
			t.Errorf("Windows declaration set included %s", rejected)
		}
	}
}

func TestWindowsNativeManifestValidates(t *testing.T) {
	if err := Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestWindowsNativeValidateRejectsInvalidManifests(t *testing.T) {
	original := Packages
	t.Cleanup(func() { Packages = original })

	tests := []struct {
		name     string
		packages []Package
	}{
		{name: "empty manifest"},
		{
			name:     "empty package path",
			packages: []Package{{Path: "", Pattern: `^(TestValid)$`}},
		},
		{
			name: "duplicate package path",
			packages: []Package{
				{Path: "./internal/session", Pattern: `^(TestFirst)$`},
				{Path: "./internal/session", Pattern: `^(TestSecond)$`},
			},
		},
		{
			name:     "unanchored pattern",
			packages: []Package{{Path: "./internal/session", Pattern: `TestUnanchored`}},
		},
		{
			name:     "invalid pattern",
			packages: []Package{{Path: "./internal/session", Pattern: `^(TestInvalid$`}},
		},
		{
			name:     "broad pattern",
			packages: []Package{{Path: "./internal/session", Pattern: `^(TestWindows.*)$`}},
		},
		{
			name:     "empty alternative",
			packages: []Package{{Path: "./internal/session", Pattern: `^(TestWindowsRootParentAndHomeTransitions||TestValidateCPWindowsCrossVolumeUsesAbsolutePath)$`}},
		},
		{
			name:     "metacharacter",
			packages: []Package{{Path: "./internal/session", Pattern: `^(TestWindowsRootParentAndHomeTransitions[0-9])$`}},
		},
		{
			name:     "subtest wildcard",
			packages: []Package{{Path: "./internal/session", Pattern: `^(TestWindowsRootParentAndHomeTransitions/.*)$`}},
		},
		{
			name:     "stale test name",
			packages: []Package{{Path: "./integration", Pattern: `^(TestWindowsResourceGateRejectsPersistentApplicationIdentity)$`}},
		},
		{
			name:     "ellipsis package",
			packages: []Package{{Path: "./...", Pattern: `^(TestWindowsRootParentAndHomeTransitions)$`}},
		},
		{
			name:     "traversal package",
			packages: []Package{{Path: "./internal/session/../session", Pattern: `^(TestWindowsRootParentAndHomeTransitions)$`}},
		},
		{
			name:     "absolute package",
			packages: []Package{{Path: `C:\\Windows\\System32`, Pattern: `^(TestWindowsRootParentAndHomeTransitions)$`}},
		},
		{
			name:     "flag-like package",
			packages: []Package{{Path: "-run", Pattern: `^(TestWindowsRootParentAndHomeTransitions)$`}},
		},
		{
			name:     "unknown package",
			packages: []Package{{Path: "./internal/not-approved", Pattern: `^(TestWindowsRootParentAndHomeTransitions)$`}},
		},
		{
			name:     "Unix authority",
			packages: []Package{{Path: "./internal/session", Pattern: `^(TestKqueue)$`}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			Packages = tt.packages
			if err := Validate(); err == nil {
				t.Fatal("Validate returned nil for invalid manifest")
			}
		})
	}
}

func patternAlternatives(pattern string) []string {
	if !strings.HasPrefix(pattern, "^(") || !strings.HasSuffix(pattern, ")$") {
		return nil
	}
	body := strings.TrimSuffix(strings.TrimPrefix(pattern, "^("), ")$")
	return strings.Split(body, "|")
}

func writeFixtureFile(t *testing.T, directory, name, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(directory, name), []byte(contents), 0o600); err != nil {
		t.Fatalf("write fixture %s: %v", name, err)
	}
}

func declaredTestNames(t *testing.T, packageDir string) map[string]bool {
	t.Helper()
	entries, err := os.ReadDir(packageDir)
	if err != nil {
		t.Fatalf("read package %s: %v", packageDir, err)
	}

	buildContext := build.Default
	buildContext.GOOS = "windows"
	buildContext.GOARCH = "amd64"

	declared := make(map[string]bool)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		matched, err := buildContext.MatchFile(packageDir, entry.Name())
		if err != nil {
			t.Fatalf("match %s: %v", filepath.Join(packageDir, entry.Name()), err)
		}
		if !matched {
			continue
		}
		path := filepath.Join(packageDir, entry.Name())
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if ok && isGoTestFunction(function) {
				declared[function.Name.Name] = true
			}
		}
	}
	return declared
}

func isGoTestFunction(function *ast.FuncDecl) bool {
	if function.Recv != nil || !strings.HasPrefix(function.Name.Name, "Test") {
		return false
	}
	if function.Type.TypeParams != nil && len(function.Type.TypeParams.List) > 0 {
		return false
	}
	if function.Type.Results != nil && len(function.Type.Results.List) > 0 {
		return false
	}
	if function.Type.Params == nil || len(function.Type.Params.List) != 1 {
		return false
	}
	parameter := function.Type.Params.List[0]
	if len(parameter.Names) > 1 {
		return false
	}
	star, ok := parameter.Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	selector, ok := star.X.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "T" {
		return false
	}
	packageName, ok := selector.X.(*ast.Ident)
	return ok && packageName.Name == "testing"
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	for {
		if info, err := os.Stat(filepath.Join(directory, "go.mod")); err == nil && !info.IsDir() {
			return directory
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			t.Fatal("could not locate module root")
		}
		directory = parent
	}
}
