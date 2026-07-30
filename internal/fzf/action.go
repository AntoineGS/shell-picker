package fzf

import (
	"errors"
	"fmt"
	"strings"

	"github.com/AntoineGS/shell-picker/internal/protocol"
)

type action struct{ text string }

var (
	navigationKeys = []string{"ctrl-l", "tab", "right", "ctrl-h", "left", "/", "~"}
	normalKeys     = []string{"h", "j", "k", "l", "i", "a", "q", "space"}
)

func sequence(actions ...action) string {
	parts := make([]string, 0, len(actions))
	for _, item := range actions {
		if item.text != "" {
			parts = append(parts, item.text)
		}
	}
	return strings.Join(parts, "+")
}

func enableSearch() action  { return action{text: "enable-search"} }
func disableSearch() action { return action{text: "disable-search"} }
func clearQuery() action    { return action{text: "clear-query"} }
func clearMulti() action    { return action{text: "clear-multi"} }
func acceptEnter() action   { return action{text: "print(enter)+accept"} }
func ignore() action        { return action{text: "ignore"} }
func wait() action          { return action{text: "wait"} }
func first() action         { return action{text: "first"} }
func down() action          { return action{text: "down"} }
func up() action            { return action{text: "up"} }
func abort() action         { return action{text: "abort"} }
func toggle() action        { return action{text: "toggle"} }

func rebind(mode protocol.Mode) action {
	switch mode {
	case protocol.ModeInsert:
		return keyAction("rebind", navigationKeys)
	case protocol.ModeNormal:
		return keyAction("rebind", appendKeys(navigationKeys, normalKeys))
	case protocol.ModeAdd:
		return action{}
	default:
		return action{}
	}
}

func unbind(mode protocol.Mode) action {
	switch mode {
	case protocol.ModeInsert:
		return keyAction("unbind", normalKeys)
	case protocol.ModeNormal:
		return action{}
	case protocol.ModeAdd:
		return keyAction("unbind", appendKeys(navigationKeys, normalKeys))
	default:
		return action{}
	}
}

func startUnbind() action { return keyAction("unbind", normalKeys) }

func keyAction(name string, keys []string) action {
	return action{text: name + "(" + strings.Join(keys, ",") + ")"}
}

func appendKeys(groups ...[]string) []string {
	var size int
	for _, group := range groups {
		size += len(group)
	}
	result := make([]string, 0, size)
	for _, group := range groups {
		result = append(result, group...)
	}
	return result
}

func reload(generation uint64) action {
	return action{text: fmt.Sprintf("reload-sync(l:%d)", generation)}
}

func changeModePrompt(prompt string) (action, error) {
	switch prompt {
	case "[I] ", "[N] ", "[A] ", "[A!] ":
		return action{text: "change-prompt(" + prompt + ")"}, nil
	default:
		return action{}, errors.New("fzf: invalid mode prompt")
	}
}

func changeHeader(header string) (action, error) {
	if header == "" {
		return action{}, errors.New("fzf: empty header")
	}
	if err := validateActionArgument(header); err != nil {
		return action{}, err
	}
	return action{text: "change-header:" + header}, nil
}

func put(text string) (action, error) {
	if text != "/" && text != "~" {
		return action{}, errors.New("fzf: put accepts only slash or tilde")
	}
	return action{text: "put(" + text + ")"}, nil
}

func validateActionArgument(argument string) error {
	if strings.ContainsAny(argument, "\r\n\x00") {
		return errors.New("fzf: action argument contains a control delimiter")
	}
	return nil
}

func transformEvent(opcode protocol.Opcode) action {
	return action{text: "transform(e:" + string(opcode) + ")"}
}

func trigger(key string) action { return action{text: "trigger(" + key + ")"} }

func binding(keys string, actions ...action) string {
	return "--bind=" + keys + ":" + sequence(actions...)
}

func RenderEffect(effect protocol.Effect) (string, error) {
	actions := make([]action, 0, 12)
	switch effect.Search {
	case "":
	case "on":
		actions = append(actions, enableSearch())
	case "off":
		actions = append(actions, disableSearch())
	default:
		return "", fmt.Errorf("fzf: invalid search state %q", effect.Search)
	}
	if effect.Rebind != "" {
		if effect.Rebind != protocol.ModeInsert && effect.Rebind != protocol.ModeNormal && effect.Rebind != protocol.ModeAdd {
			return "", fmt.Errorf("fzf: invalid rebind mode %q", effect.Rebind)
		}
		actions = append(actions, rebind(effect.Rebind), unbind(effect.Rebind))
	}
	if effect.ClearMulti {
		actions = append(actions, clearMulti())
	}
	if effect.ReloadGeneration != 0 {
		actions = append(actions, reload(effect.ReloadGeneration))
	}
	if effect.ClearQuery {
		actions = append(actions, clearQuery())
	}
	if effect.Put != "" {
		putAction, err := put(effect.Put)
		if err != nil {
			return "", err
		}
		actions = append(actions, putAction)
	}
	if effect.Accept {
		actions = append(actions, acceptEnter())
	}
	if effect.Ignore {
		actions = append(actions, ignore())
	}
	if effect.ReloadGeneration != 0 {
		actions = append(actions, wait(), first())
	}
	if effect.Prompt != "" {
		prompt, err := changeModePrompt(effect.Prompt)
		if err != nil {
			return "", err
		}
		actions = append(actions, prompt)
	}
	if effect.Header != "" {
		header, err := changeHeader(effect.Header)
		if err != nil {
			return "", err
		}
		actions = append(actions, header)
	}
	return sequence(actions...), nil
}
