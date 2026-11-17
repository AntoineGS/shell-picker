package fzf

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestCompletionEffectKeepsChangeHandlerAndLiteralText(t *testing.T) {
	text := "a)+abort+execute(touch unwanted)"
	rendered, err := RenderEffect(protocol.Effect{
		Completion: true, ReloadGeneration: 2, Prompt: "[N] ",
		Query: "../" + text, SetQuery: true, SearchQuery: text, SetSearchQuery: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rendered, text) || strings.Contains(rendered, "unbind(change)") {
		t.Fatalf("unsafe literal or disabled path editing: %s", rendered)
	}
	for _, value := range []string{"../" + text, text} {
		if !strings.Contains(rendered, "v:"+base64.RawURLEncoding.EncodeToString([]byte(value))) {
			t.Fatalf("missing encoded value %q: %s", value, rendered)
		}
	}
}
