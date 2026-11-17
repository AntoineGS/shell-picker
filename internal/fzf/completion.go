package fzf

import "encoding/base64"

// Local literal callbacks keep arbitrary path/prompt bytes out of fzf's action
// grammar, even when several text-changing actions must share one response.
func literalTransform(kind, text string) action {
	return action{text: "transform-" + kind + "(v:" + base64.RawURLEncoding.EncodeToString([]byte(text)) + ")"}
}

func buildNormalPrintableKeys() []string {
	active := map[rune]bool{
		'h': true, 'j': true, 'k': true, 'l': true, 'i': true, 'a': true, 'q': true,
		'g': true, 'G': true,
		'/': true, '~': true, ',': true, '.': true,
	}
	keys := make([]string, 0, 83)
	for key := '!'; key <= '~'; key++ {
		if !active[key] {
			keys = append(keys, string(key))
		}
	}
	return keys
}
