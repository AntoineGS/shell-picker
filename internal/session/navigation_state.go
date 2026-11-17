package session

import (
	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/pathutil"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func setNavigationUnchecked(proposal *ProposedTransition, target pathutil.Location) {
	proposal.State.Location = cloneLocation(target)
	proposal.State.Query = nil
	if proposal.State.Source == protocol.SourceBrowser {
		if proposal.State.Browser == nil {
			proposal.State.Browser = &BrowserContext{}
		}
		proposal.State.Browser.Location = cloneLocation(target)
		proposal.State.Browser.Mode = proposal.State.Mode
		proposal.State.Browser.Query = nil
		proposal.State.Browser.SelectedPaths = [][]byte{}
		proposal.State.Browser.records = nil
	}
	if proposal.State.Mode == protocol.ModeAdd {
		proposal.State.Mode = protocol.ModeNormal
	}
	proposal.State.AddError = false
	proposal.State.Prompt = modePrompt(proposal.State.Mode, false)
	proposal.Build = &candidate.BuildRequest{Picker: proposal.State.Picker, Location: cloneLocation(target)}
	proposal.Effect = modeEffect(proposal.State, true, 0)
	proposal.Effect.Header = pathutil.PromptDisplayHome(target, proposal.State.Home)
	proposal.Effect.ClearMulti = true
}
