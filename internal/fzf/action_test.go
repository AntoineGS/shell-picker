package fzf

import (
	"strings"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestRenderNavigationEffect(t *testing.T) {
	got, err := RenderEffect(protocol.Effect{Mode: protocol.ModeNormal, Prompt: `[N] a\)b/ `, ClearMulti: true, ClearQuery: true, ReloadGeneration: 7})
	want := `clear-multi+reload-sync(l:7)+clear-query+wait+first+change-prompt:[N] a\)b/ `
	if err != nil || got != want {
		t.Fatalf("got=%q want=%q err=%v", got, want, err)
	}
}

func TestRenderModeEffects(t *testing.T) {
	tests := []struct {
		name   string
		effect protocol.Effect
		want   string
	}{
		{"insert", protocol.Effect{Search: "on", Rebind: protocol.ModeInsert, Prompt: "[I] /work/ "},
			"enable-search+rebind(ctrl-l,tab,right,ctrl-h,left,/,~)+unbind(h,j,k,l,i,a,q,space)+change-prompt:[I] /work/ "},
		{"normal", protocol.Effect{Search: "off", Rebind: protocol.ModeNormal, Prompt: "[N] /work/ "},
			"disable-search+rebind(ctrl-l,tab,right,ctrl-h,left,/,~,h,j,k,l,i,a,q,space)+change-prompt:[N] /work/ "},
		{"add", protocol.Effect{Search: "on", Rebind: protocol.ModeAdd, Prompt: "[A] /work/ ", ClearQuery: true},
			"enable-search+unbind(ctrl-l,tab,right,ctrl-h,left,/,~,h,j,k,l,i,a,q,space)+clear-query+change-prompt:[A] /work/ "},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := RenderEffect(test.effect)
			if err != nil || got != test.want {
				t.Fatalf("got=%q want=%q err=%v", got, test.want, err)
			}
		})
	}
}

func TestRenderTerminalEffects(t *testing.T) {
	tests := []struct {
		name   string
		effect protocol.Effect
		want   string
	}{
		{"accept", protocol.Effect{Accept: true}, "print(enter)+accept"},
		{"normal escape", protocol.Effect{ClearMulti: true}, "clear-multi"},
		{"put", protocol.Effect{Put: "/"}, "put(/)"},
		{"ignore", protocol.Effect{Ignore: true}, "ignore"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := RenderEffect(test.effect)
			if err != nil || got != test.want {
				t.Fatalf("got=%q want=%q err=%v", got, test.want, err)
			}
		})
	}
}

func TestActionArgumentsRejectControlsAndPutVocabulary(t *testing.T) {
	for _, raw := range []string{"x\rdown", "x\naccept", "x\x00y"} {
		if _, err := changePrompt(raw); err == nil {
			t.Fatalf("accepted control input %q", raw)
		}
	}
	for _, raw := range []string{"", ".", "x)+execute(id)"} {
		if _, err := put(raw); err == nil {
			t.Fatalf("accepted put input %q", raw)
		}
	}
}

func TestActionArgumentDelimiterCorpusCannotInjectAction(t *testing.T) {
	for _, raw := range []string{
		"x)+execute(id)", `x\\)+reload(e:en)`, "x,y:z", "{q}", "$(id)", "transform(e:en)", "accept+abort",
	} {
		action, err := changePrompt(raw)
		if err != nil {
			t.Fatalf("changePrompt(%q): %v", raw, err)
		}
		assertTerminalPromptAction(t, action.text, raw)
	}
}

func FuzzActionArgumentsRejectInjection(f *testing.F) {
	for _, seed := range []string{"ok", "x)+execute(id)", "x\rdown", "x\naccept", "x\x00y", `x\\)+reload(e:en)`} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		action, err := changePrompt(raw)
		if strings.ContainsAny(raw, "\r\n\x00") {
			if err == nil {
				t.Fatalf("accepted control input %q as %q", raw, action.text)
			}
			return
		}
		if err == nil {
			assertTerminalPromptAction(t, action.text, raw)
		}
	})
}

func TestWindowsPromptBackslashIsPreservedByTerminalAction(t *testing.T) {
	got, err := changePrompt(`[N] C:\ `)
	if err != nil {
		t.Fatal(err)
	}
	assertTerminalPromptAction(t, got.text, `[N] C:\ `)
}

func assertTerminalPromptAction(t *testing.T, rendered, prompt string) {
	t.Helper()
	if want := "change-prompt:" + prompt; rendered != want {
		t.Fatalf("action=%q want terminal action=%q", rendered, want)
	}
}
