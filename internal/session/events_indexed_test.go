package session

import (
	"context"
	"errors"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/pathutil"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestSourceToggleSavesAndRestoresBrowserContext(t *testing.T) {
	selected := eventRecord(protocol.KindFile, "report.txt", "/work/report.txt")
	snapshot := eventSnapshot(protocol.PickerCP, protocol.ModeNormal,
		pathutil.Filesystem([]byte(sessionTestPath("/work"))), selected)
	snapshot.state.Query = []byte("reports")
	snapshot.state.Browser = &BrowserContext{
		Location: cloneLocation(snapshot.state.Location), Mode: protocol.ModeNormal,
		Query: []byte("reports"), SelectedPaths: [][]byte{[]byte(sessionTestPath("/work/report.txt"))},
	}

	entered, err := Reduce(snapshot, protocol.Event{Opcode: protocol.OpSourceToggle, Query: []byte("reports")})
	if err != nil {
		t.Fatal(err)
	}
	indexed := entered.proposalForTest()
	if indexed.State.Source != protocol.SourceIndexed || indexed.State.Mode != protocol.ModeInsert ||
		string(indexed.State.Query) != "reports" || indexed.Build == nil ||
		indexed.Build.Source != protocol.SourceIndexed {
		t.Fatalf("entering indexed view = %+v", indexed)
	}
	if indexed.State.Browser == nil || indexed.State.Browser.Mode != protocol.ModeNormal ||
		string(indexed.State.Browser.Query) != "reports" ||
		string(indexed.State.Browser.Location.Path) != sessionTestPath("/work") ||
		len(indexed.State.Browser.SelectedPaths) != 1 ||
		string(indexed.State.Browser.SelectedPaths[0]) != sessionTestPath("/work/report.txt") {
		t.Fatalf("saved browser context = %+v", indexed.State.Browser)
	}

	indexedSnapshot := Snapshot{generation: 8, state: indexed.State, records: ownRecordSet(nil)}
	returned, err := Reduce(indexedSnapshot, protocol.Event{Opcode: protocol.OpSourceToggle, Query: []byte("quarter")})
	if err != nil {
		t.Fatal(err)
	}
	browser := returned.proposalForTest()
	if browser.State.Source != protocol.SourceBrowser || browser.State.Mode != protocol.ModeNormal ||
		string(browser.State.Query) != "reports" || browser.Build == nil ||
		browser.Build.Source != protocol.SourceBrowser ||
		string(browser.Build.Location.Path) != sessionTestPath("/work") {
		t.Fatalf("returning to browser = %+v", browser)
	}
}

func TestSourceToggleDoesNotPromoteFocusedFallbackToASelection(t *testing.T) {
	focused := eventRecord(protocol.KindFile, "old-focus", "/work/old-focus")
	newFocus := eventRecord(protocol.KindFile, "new-focus", "/work/new-focus")
	snapshot := eventSnapshot(protocol.PickerCP, protocol.ModeInsert,
		pathutil.Filesystem([]byte(sessionTestPath("/work"))), focused, newFocus)
	snapshot.state.Browser = &BrowserContext{
		Location: cloneLocation(snapshot.state.Location), Mode: protocol.ModeInsert, SelectedPaths: [][]byte{},
	}

	entered, err := Reduce(snapshot, protocol.Event{
		Opcode: protocol.OpSourceToggle, SelectedRecords: [][]byte{[]byte(focused.FullKey())},
	})
	if err != nil {
		t.Fatal(err)
	}
	indexed := entered.proposalForTest()
	if len(indexed.State.Browser.SelectedPaths) != 0 || indexed.State.Indexed == nil ||
		len(indexed.State.Indexed.SelectedPaths) != 0 {
		t.Fatalf("source toggle manufactured a mark: browser=%+v indexed=%+v",
			indexed.State.Browser, indexed.State.Indexed)
	}

	returned, err := Reduce(Snapshot{generation: snapshot.generation, state: indexed.State, records: snapshot.records},
		protocol.Event{Opcode: protocol.OpSourceToggle, SelectedRecords: [][]byte{[]byte(focused.FullKey())}})
	if err != nil {
		t.Fatal(err)
	}
	browser := returned.proposalForTest()
	if len(browser.State.Browser.SelectedPaths) != 0 {
		t.Fatalf("return switch manufactured browser marks: %q", browser.State.Browser.SelectedPaths)
	}
	outcome, err := ValidateCP(Snapshot{generation: snapshot.generation, state: browser.State, records: snapshot.records},
		[][]byte{[]byte(newFocus.FullKey())}, []byte(sessionTestPath("/work")))
	if err != nil || len(outcome.Paths) != 1 ||
		string(outcome.Paths[0]) != "new-focus" {
		t.Fatalf("new focus outcome=%+v err=%v", outcome, err)
	}
}

func TestMarkToggleCachesSelectionsIndependentlyFromTheFZFQuery(t *testing.T) {
	alpha := eventRecord(protocol.KindFile, "alpha.txt", "/work/alpha.txt")
	beta := eventRecord(protocol.KindFile, "beta.txt", "/work/beta.txt")
	snapshot := eventSnapshot(protocol.PickerCP, protocol.ModeInsert,
		pathutil.Filesystem([]byte(sessionTestPath("/work"))), alpha, beta)
	snapshot.state.Source = protocol.SourceIndexed
	snapshot.state.Indexed = &IndexedContext{Query: []byte("alpha")}

	reduction, err := Reduce(snapshot, protocol.Event{
		Opcode: protocol.OpMarkToggle, CurrentItem: []byte(beta.FullKey()), Query: []byte("alpha"),
	})
	if err != nil {
		t.Fatal(err)
	}
	proposal := reduction.proposalForTest()
	if proposal.Build != nil || !proposal.Effect.ToggleSelection ||
		proposal.State.Indexed == nil || string(proposal.State.Indexed.Query) != "alpha" ||
		len(proposal.State.Indexed.SelectedPaths) != 1 ||
		string(proposal.State.Indexed.SelectedPaths[0]) != sessionTestPath("/work/beta.txt") {
		t.Fatalf("mark toggle proposal = %+v", proposal)
	}

	deselected, err := Reduce(Snapshot{generation: snapshot.generation, state: proposal.State, records: snapshot.records},
		protocol.Event{Opcode: protocol.OpMarkToggle, CurrentItem: []byte(beta.FullKey())})
	if err != nil {
		t.Fatal(err)
	}
	deselectedProposal := deselected.proposalForTest()
	if got := deselectedProposal.State.Indexed.SelectedPaths; len(got) != 0 {
		t.Fatalf("deselected path remains cached: %q", got)
	}
	if !deselectedProposal.Effect.ToggleSelection || deselectedProposal.Effect.Ignore {
		t.Fatalf("unmark must toggle fzf after updating the cache: %+v", deselectedProposal.Effect)
	}
}

func TestCDMarkToggleReplacesSingleSelection(t *testing.T) {
	first := eventRecord(protocol.KindDirectory, "first", "/work/first")
	second := eventRecord(protocol.KindDirectory, "second", "/work/second")
	snapshot := eventSnapshot(protocol.PickerCD, protocol.ModeNormal,
		pathutil.Filesystem([]byte(sessionTestPath("/work"))), first, second)
	snapshot.state.Browser = &BrowserContext{
		Location: cloneLocation(snapshot.state.Location), Mode: protocol.ModeNormal,
		SelectedPaths: [][]byte{[]byte(sessionTestPath("/work/first"))},
	}

	reduction, err := Reduce(snapshot, protocol.Event{
		Opcode: protocol.OpMarkToggle, CurrentItem: []byte(second.FullKey()), Query: []byte(""),
	})
	if err != nil {
		t.Fatal(err)
	}
	proposal := reduction.proposalForTest()
	got := proposal.State.Browser.SelectedPaths
	if len(got) != 1 || string(got[0]) != sessionTestPath("/work/second") ||
		!proposal.Effect.ClearMulti || !proposal.Effect.ToggleSelection || proposal.Effect.Ignore {
		t.Fatalf("single cd mark proposal = %+v", proposal)
	}

	reduction, err = Reduce(Snapshot{generation: snapshot.generation, state: proposal.State, records: snapshot.records},
		protocol.Event{Opcode: protocol.OpMarkToggle, CurrentItem: []byte(second.FullKey())})
	if err != nil {
		t.Fatal(err)
	}
	proposal = reduction.proposalForTest()
	if len(proposal.State.Browser.SelectedPaths) != 0 ||
		proposal.Effect.ClearMulti || !proposal.Effect.ToggleSelection {
		t.Fatalf("unmark cd selection proposal = %+v", proposal)
	}
}

func TestBrowserNavigationClearsBrowserMarksWithNativeMultiSelection(t *testing.T) {
	directory := eventRecord(protocol.KindDirectory, "child", "/work/child")
	selected := eventRecord(protocol.KindFile, "file", "/work/file")
	tests := []struct {
		name  string
		event protocol.Event
	}{
		{"forward", protocol.Event{Opcode: protocol.OpForward, CurrentItem: []byte(directory.FullKey())}},
		{"parent", protocol.Event{Opcode: protocol.OpParent}},
		{"slash", protocol.Event{Opcode: protocol.OpSlash}},
		{"home", protocol.Event{Opcode: protocol.OpHome}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := eventSnapshot(protocol.PickerCP, protocol.ModeNormal,
				pathutil.Filesystem([]byte(sessionTestPath("/work"))), directory, selected)
			snapshot.state.Browser = &BrowserContext{
				Location: cloneLocation(snapshot.state.Location), Mode: protocol.ModeNormal,
				SelectedPaths: [][]byte{[]byte(sessionTestPath("/work/file"))},
			}
			reduction, err := Reduce(snapshot, test.event)
			if err != nil {
				t.Fatal(err)
			}
			proposal := reduction.proposalForTest()
			if !proposal.Effect.ClearMulti || proposal.State.Browser == nil ||
				len(proposal.State.Browser.SelectedPaths) != 0 {
				t.Fatalf("navigation proposal = %+v", proposal)
			}
		})
	}
}

func TestSourceToggleDoesNotTrustSelectedFileFallback(t *testing.T) {
	snapshot := eventSnapshot(protocol.PickerCP, protocol.ModeInsert,
		pathutil.Filesystem([]byte(sessionTestPath("/work"))))
	snapshot.state.Browser = &BrowserContext{Location: cloneLocation(snapshot.state.Location),
		Mode: protocol.ModeInsert, SelectedPaths: [][]byte{}}
	reduction, err := Reduce(snapshot, protocol.Event{Opcode: protocol.OpSourceToggle,
		SelectedRecords: [][]byte{[]byte(eventRecord(protocol.KindFile, "stale", "/work/stale").FullKey())}})
	if err != nil {
		t.Fatal(err)
	}
	if got := reduction.proposalForTest().State.Browser.SelectedPaths; len(got) != 0 {
		t.Fatalf("selected-file fallback created cached marks: %q", got)
	}
}

func TestSourceSwitchRestoresSelectionPositionsFromSavedSource(t *testing.T) {
	for _, test := range []struct {
		name   string
		picker protocol.Picker
		kind   protocol.Kind
	}{
		{name: "cp", picker: protocol.PickerCP, kind: protocol.KindFile},
		{name: "cd", picker: protocol.PickerCD, kind: protocol.KindDirectory},
	} {
		t.Run(test.name, func(t *testing.T) {
			first := eventRecord(test.kind, "one", "/work/one")
			second := eventRecord(test.kind, "two", "/work/two")
			actor := New(context.Background(), func(_ context.Context, request candidate.BuildRequest) (candidate.BuildResult, error) {
				return candidate.BuildResult{Records: []candidate.Record{first, second}}, nil
			})
			defer actor.Close()
			state := eventSnapshot(test.picker, protocol.ModeInsert,
				pathutil.Filesystem([]byte(sessionTestPath("/work"))), first, second).state
			state.Indexed = &IndexedContext{Query: []byte("two"),
				SelectedPaths: [][]byte{[]byte(sessionTestPath("/work/two"))}}
			initial, err := actor.Apply(context.Background(), ProposedTransition{
				State: state, Build: &candidate.BuildRequest{Picker: test.picker, Location: state.Location},
			})
			if err != nil {
				t.Fatal(err)
			}
			result, err := Handle(context.Background(), actor,
				protocol.Event{Opcode: protocol.OpSourceToggle, Query: []byte("one")})
			if err != nil {
				t.Fatal(err)
			}
			if result.Snapshot.Generation() != initial.Snapshot.Generation()+1 ||
				result.Effect.RestorePositions != "2" {
				t.Fatalf("source restoration effect = %+v", result.Effect)
			}
		})
	}
}

func TestCDSourceRoundTripPreservesOneMarkedDirectoryPerSource(t *testing.T) {
	browserRecord := eventRecord(protocol.KindDirectory, "browser", "/work/browser")
	indexedRecord := eventRecord(protocol.KindDirectory, "indexed", "/work/indexed")
	snapshot := eventSnapshot(protocol.PickerCD, protocol.ModeNormal,
		pathutil.Filesystem([]byte(sessionTestPath("/work"))), browserRecord, indexedRecord)
	snapshot.state.Browser = &BrowserContext{
		Location: cloneLocation(snapshot.state.Location), Mode: protocol.ModeNormal,
		SelectedPaths: [][]byte{[]byte(sessionTestPath("/work/browser"))},
	}
	snapshot.state.Indexed = &IndexedContext{
		SelectedPaths: [][]byte{[]byte(sessionTestPath("/work/indexed"))},
	}

	toIndexed, err := Reduce(snapshot, protocol.Event{Opcode: protocol.OpSourceToggle, Query: []byte("needle")})
	if err != nil {
		t.Fatal(err)
	}
	indexedState := toIndexed.proposalForTest().State
	if indexedState.Source != protocol.SourceIndexed ||
		len(indexedState.Browser.SelectedPaths) != 1 ||
		string(indexedState.Browser.SelectedPaths[0]) != sessionTestPath("/work/browser") ||
		len(indexedState.Indexed.SelectedPaths) != 1 ||
		string(indexedState.Indexed.SelectedPaths[0]) != sessionTestPath("/work/indexed") {
		t.Fatalf("browser to indexed state = %+v", indexedState)
	}

	indexedSnapshot := eventSnapshot(protocol.PickerCD, indexedState.Mode,
		indexedState.Location, browserRecord, indexedRecord)
	indexedSnapshot.state = indexedState
	toBrowser, err := Reduce(indexedSnapshot, protocol.Event{Opcode: protocol.OpSourceToggle, Query: []byte("query")})
	if err != nil {
		t.Fatal(err)
	}
	browserState := toBrowser.proposalForTest().State
	if browserState.Source != protocol.SourceBrowser ||
		len(browserState.Browser.SelectedPaths) != 1 ||
		string(browserState.Browser.SelectedPaths[0]) != sessionTestPath("/work/browser") ||
		len(browserState.Indexed.SelectedPaths) != 1 ||
		string(browserState.Indexed.SelectedPaths[0]) != sessionTestPath("/work/indexed") {
		t.Fatalf("indexed to browser state = %+v", browserState)
	}
}

func TestCDSourceSwitchRestoresNativeSelectionForEachSource(t *testing.T) {
	first := eventRecord(protocol.KindDirectory, "browser", "/work/browser")
	second := eventRecord(protocol.KindDirectory, "indexed", "/work/indexed")
	actor := New(context.Background(), func(_ context.Context, _ candidate.BuildRequest) (candidate.BuildResult, error) {
		return candidate.BuildResult{Records: []candidate.Record{first, second}}, nil
	})
	defer actor.Close()

	state := eventSnapshot(protocol.PickerCD, protocol.ModeNormal,
		pathutil.Filesystem([]byte(sessionTestPath("/work"))), first, second).state
	state.Browser = &BrowserContext{
		Location: cloneLocation(state.Location), Mode: protocol.ModeNormal,
		SelectedPaths: [][]byte{[]byte(sessionTestPath("/work/browser"))},
	}
	state.Indexed = &IndexedContext{
		SelectedPaths: [][]byte{[]byte(sessionTestPath("/work/indexed"))},
	}
	if _, err := actor.Apply(context.Background(), ProposedTransition{
		State: state, Build: &candidate.BuildRequest{Picker: protocol.PickerCD, Location: state.Location},
	}); err != nil {
		t.Fatal(err)
	}

	indexed, err := Handle(context.Background(), actor,
		protocol.Event{Opcode: protocol.OpSourceToggle, Query: []byte("indexed")})
	if err != nil {
		t.Fatal(err)
	}
	if indexed.Snapshot.State().Source != protocol.SourceIndexed || indexed.Effect.RestorePositions != "2" {
		t.Fatalf("indexed restore: state=%+v effect=%+v", indexed.Snapshot.State(), indexed.Effect)
	}

	browser, err := Handle(context.Background(), actor,
		protocol.Event{Opcode: protocol.OpSourceToggle, Query: []byte("browser")})
	if err != nil {
		t.Fatal(err)
	}
	if browser.Snapshot.State().Source != protocol.SourceBrowser || browser.Effect.RestorePositions != "1" {
		t.Fatalf("browser restore: state=%+v effect=%+v", browser.Snapshot.State(), browser.Effect)
	}
}

func TestIndexedForwardEntersBrowserOnlyForDirectory(t *testing.T) {
	directory := eventRecord(protocol.KindDirectory, "/work/dir", "/work/dir")
	file := eventRecord(protocol.KindFile, "/work/file", "/work/file")
	snapshot := eventSnapshot(protocol.PickerCP, protocol.ModeInsert,
		pathutil.Filesystem([]byte(sessionTestPath("/work"))), directory, file)
	snapshot.state.Source = protocol.SourceIndexed
	snapshot.state.Indexed = &IndexedContext{SelectedPaths: [][]byte{[]byte(sessionTestPath("/work/file"))}}

	forwardFile, err := Reduce(snapshot, protocol.Event{Opcode: protocol.OpForward, CurrentItem: []byte(file.FullKey())})
	if err != nil {
		t.Fatal(err)
	}
	if effect := forwardFile.proposalForTest().Effect; !effect.Ignore {
		t.Fatalf("forward on file effect = %+v, want ignore", effect)
	}

	forwardDirectory, err := Reduce(snapshot, protocol.Event{
		Opcode: protocol.OpForward, CurrentItem: []byte(directory.FullKey()), Query: []byte("alpha"),
	})
	if err != nil {
		t.Fatal(err)
	}
	proposal := forwardDirectory.proposalForTest()
	if proposal.State.Source != protocol.SourceBrowser || proposal.Build == nil ||
		proposal.Build.Source != protocol.SourceBrowser ||
		string(proposal.Build.Location.Path) != sessionTestPath("/work/dir") ||
		proposal.State.Indexed == nil ||
		string(proposal.State.Indexed.Query) != "alpha" ||
		len(proposal.State.Indexed.SelectedPaths) != 1 ||
		string(proposal.State.Indexed.SelectedPaths[0]) != sessionTestPath("/work/file") {
		t.Fatalf("forward on directory proposal = %+v", proposal)
	}
}

func TestIndexedParentIsIgnoredWithoutChangingSourceOrGenerationRequest(t *testing.T) {
	for _, mode := range []protocol.Mode{protocol.ModeNormal, protocol.ModeInsert} {
		snapshot := eventSnapshot(protocol.PickerCP, mode,
			pathutil.Filesystem([]byte(sessionTestPath("/work"))),
			eventRecord(protocol.KindDirectory, "/work/dir", "/work/dir"))
		snapshot.state.Source = protocol.SourceIndexed
		reduction, err := Reduce(snapshot, protocol.Event{Opcode: protocol.OpParent})
		if err != nil {
			t.Fatal(err)
		}
		proposal := reduction.proposalForTest()
		if !proposal.Effect.Ignore || proposal.Build != nil || proposal.State.Source != protocol.SourceIndexed {
			t.Fatalf("%s parent changed indexed navigation state: %+v", mode, proposal)
		}
	}
}

func TestFailedIndexedSwitchLeavesBrowserUsableForRetry(t *testing.T) {
	snapshot := eventSnapshot(protocol.PickerCP, protocol.ModeInsert,
		pathutil.Filesystem([]byte(sessionTestPath("/work"))))
	actor := New(context.Background(), func(_ context.Context, request candidate.BuildRequest) (candidate.BuildResult, error) {
		if request.Source == protocol.SourceIndexed {
			return candidate.BuildResult{}, errors.New("database is busy")
		}
		return candidate.BuildResult{}, nil
	})
	defer actor.Close()
	initial, err := actor.Apply(context.Background(), ProposedTransition{
		State: snapshot.state,
		Build: &candidate.BuildRequest{Picker: protocol.PickerCP, Location: snapshot.state.Location},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := Handle(context.Background(), actor, protocol.Event{Opcode: protocol.OpSourceToggle})
	if err != nil {
		t.Fatal(err)
	}
	if result.Effect.Header != "Unearth unavailable — Ctrl+F to retry" || !result.Effect.ErrorPrompt ||
		result.Effect.ReloadGeneration != 0 || result.Snapshot.State().Source != protocol.SourceBrowser ||
		result.Snapshot.Generation() != initial.Snapshot.Generation() {
		t.Fatalf("failed transition result = %+v", result)
	}
}
