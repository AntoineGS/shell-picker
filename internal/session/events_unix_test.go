//go:build !windows

package session

import (
	"testing"

	"github.com/AntoineGS/shell-picker/internal/pathutil"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestUnixRootParentAndHomeTransitions(t *testing.T) {
	home := pathutil.Filesystem([]byte("/home/test"))
	for _, test := range []struct {
		name     string
		location pathutil.Location
		event    protocol.Event
		want     string
	}{
		{"root clamp", pathutil.Filesystem([]byte("/")), protocol.Event{Opcode: protocol.OpParent}, "/"},
		{"Drives parent", pathutil.Drives(), protocol.Event{Opcode: protocol.OpParent}, "/"},
		{"filesystem parent", pathutil.Filesystem([]byte("/work/child")), protocol.Event{Opcode: protocol.OpParent}, "/work"},
		{"slash root", pathutil.Filesystem([]byte("/work")), protocol.Event{Opcode: protocol.OpSlash}, "/"},
		{"home", pathutil.Filesystem([]byte("/work")), protocol.Event{Opcode: protocol.OpHome}, "/home/test"},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := eventSnapshot(protocol.PickerCD, protocol.ModeNormal, test.location)
			snapshot.state.Home = home
			reduction, err := Reduce(snapshot, test.event)
			proposal := reduction.proposalForTest()
			if err != nil || proposal.Build == nil || proposal.State.Location.Kind != pathutil.KindFilesystem ||
				string(proposal.State.Location.Path) != test.want || !proposal.Effect.ClearMulti || !proposal.Effect.ClearQuery {
				t.Fatalf("proposal=%+v err=%v", proposal, err)
			}
		})
	}
}
