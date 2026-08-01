//go:build windows

package session

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/pathutil"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestWindowsRootParentAndHomeTransitions(t *testing.T) {
	home := pathutil.Filesystem([]byte(`C:\Users\test`))
	crossVolume := eventRecord(protocol.KindDirectory, "cross-volume", `D:\target`)
	for _, test := range []struct {
		name               string
		location           pathutil.Location
		event              protocol.Event
		records            []candidate.Record
		wantKind           pathutil.Kind
		wantPath           string
		wantHeader         string
		wantAbsoluteTarget bool
	}{
		{"drive root parent", pathutil.Filesystem([]byte(`C:\`)), protocol.Event{Opcode: protocol.OpParent}, nil, pathutil.KindDrives, "", "", false},
		{"UNC root parent", pathutil.Filesystem([]byte(`\\server\share\`)), protocol.Event{Opcode: protocol.OpParent}, nil, pathutil.KindDrives, "", "", false},
		{"Drives parent", pathutil.Drives(), protocol.Event{Opcode: protocol.OpParent}, nil, pathutil.KindDrives, "", "", false},
		{"filesystem parent", pathutil.Filesystem([]byte(`C:\work\child`)), protocol.Event{Opcode: protocol.OpParent}, nil, pathutil.KindFilesystem, `C:\work`, "", false},
		{"slash root", pathutil.Filesystem([]byte(`C:\work`)), protocol.Event{Opcode: protocol.OpSlash}, nil, pathutil.KindDrives, "", "", false},
		{"home", pathutil.Drives(), protocol.Event{Opcode: protocol.OpHome}, nil, pathutil.KindFilesystem, `C:\Users\test`, "", false},
		{"cross-volume selection", pathutil.Filesystem([]byte(`C:\work`)), protocol.Event{Opcode: protocol.OpForward, CurrentItem: []byte(crossVolume.FullKey())}, []candidate.Record{crossVolume}, pathutil.KindFilesystem, `D:\target`, "", true},
		{"Windows prompt separators", pathutil.Filesystem([]byte(`C:\work\child`)), protocol.Event{Opcode: protocol.OpParent}, nil, pathutil.KindFilesystem, `C:\work`, `C:\work\`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := eventSnapshot(protocol.PickerCD, protocol.ModeNormal, test.location, test.records...)
			snapshot.state.Home = home
			reduction, err := Reduce(snapshot, test.event)
			proposal := reduction.proposalForTest()
			if err != nil || proposal.Build == nil || proposal.State.Location.Kind != test.wantKind ||
				string(proposal.State.Location.Path) != test.wantPath || !proposal.Effect.ClearMulti || !proposal.Effect.ClearQuery {
				t.Fatalf("proposal=%+v err=%v", proposal, err)
			}
			if test.wantAbsoluteTarget && !filepath.IsAbs(string(proposal.State.Location.Path)) {
				t.Fatalf("cross-volume target=%q is not absolute", proposal.State.Location.Path)
			}
			if test.wantHeader != "" && (proposal.Effect.Header != test.wantHeader || !strings.Contains(proposal.Effect.Header, `\`) || strings.Contains(proposal.Effect.Header, "/")) {
				t.Fatalf("Windows prompt header=%q want=%q", proposal.Effect.Header, test.wantHeader)
			}
		})
	}
}
