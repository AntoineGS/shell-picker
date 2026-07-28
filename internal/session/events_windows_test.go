//go:build windows

package session

import (
	"testing"

	"github.com/AntoineGS/shell-picker/internal/pathutil"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestWindowsRootParentAndHomeTransitions(t *testing.T) {
	home := pathutil.Filesystem([]byte(`C:\Users\test`))
	for _, test := range []struct {
		name     string
		location pathutil.Location
		event    protocol.Event
		wantKind pathutil.Kind
		wantPath string
	}{
		{"drive root parent", pathutil.Filesystem([]byte(`C:\`)), protocol.Event{Opcode: protocol.OpParent}, pathutil.KindDrives, ""},
		{"UNC root parent", pathutil.Filesystem([]byte(`\\server\share\`)), protocol.Event{Opcode: protocol.OpParent}, pathutil.KindDrives, ""},
		{"Drives parent", pathutil.Drives(), protocol.Event{Opcode: protocol.OpParent}, pathutil.KindDrives, ""},
		{"filesystem parent", pathutil.Filesystem([]byte(`C:\work\child`)), protocol.Event{Opcode: protocol.OpParent}, pathutil.KindFilesystem, `C:\work`},
		{"slash root", pathutil.Filesystem([]byte(`C:\work`)), protocol.Event{Opcode: protocol.OpSlash}, pathutil.KindDrives, ""},
		{"home", pathutil.Drives(), protocol.Event{Opcode: protocol.OpHome}, pathutil.KindFilesystem, `C:\Users\test`},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := eventSnapshot(protocol.PickerCD, protocol.ModeNormal, test.location)
			snapshot.state.Home = home
			reduction, err := Reduce(snapshot, test.event)
			proposal := reduction.proposalForTest()
			if err != nil || proposal.Build == nil || proposal.State.Location.Kind != test.wantKind ||
				string(proposal.State.Location.Path) != test.wantPath || !proposal.Effect.ClearMulti || !proposal.Effect.ClearQuery {
				t.Fatalf("proposal=%+v err=%v", proposal, err)
			}
		})
	}
}
