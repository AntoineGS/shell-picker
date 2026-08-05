package integration

import (
	"errors"
	"fmt"
	"strings"
	"time"

	integrationpkg "github.com/AntoineGS/shell-picker/internal/integration"
)

func captureFirstFrameTimestamp() firstFrameTimestamp {
	now := time.Now()
	return firstFrameTimestamp{wall: now.UTC(), monotonic: now}
}

func firstFrameLatestEzaPreviewComplete(events []traceEvent) (traceEvent, bool) {
	for index := len(events) - 1; index >= 0; index-- {
		event := events[index]
		if event.Event == "preview.finished" && event.Outcome == "ok" && event.Renderer == "eza" && event.ChildStarts == 1 {
			return event, true
		}
	}
	return traceEvent{}, false
}

func firstFrameEzaPreviewCompleteCount(events []traceEvent) int {
	count := 0
	for _, event := range events {
		if event.Event == "preview.finished" && event.Outcome == "ok" && event.Renderer == "eza" && event.ChildStarts == 1 {
			count++
		}
	}
	return count
}

func countFirstFrameCallbackInvocations(events []traceEvent, requireTerminal bool) (firstFrameCallbackCounts, error) {
	return countFirstFrameCallbackInvocationsAt(events, time.Time{}, requireTerminal)
}

func countFirstFrameCallbackInvocationsThrough(events []traceEvent, through time.Time) (firstFrameCallbackCounts, error) {
	if through.IsZero() {
		return firstFrameCallbackCounts{}, errors.New("first-frame callback boundary is missing")
	}
	return countFirstFrameCallbackInvocationsAt(events, through, true)
}

func countFirstFrameCallbackInvocationsAt(events []traceEvent, through time.Time, requireTerminal bool) (firstFrameCallbackCounts, error) {
	if len(events) == 0 {
		return firstFrameCallbackCounts{}, errors.New("first-frame callback trace is empty")
	}
	session := events[0].Session
	if session == "" {
		return firstFrameCallbackCounts{}, errors.New("first-frame callback trace has no session")
	}
	counts := firstFrameCallbackCounts{}
	var previous time.Time
	starts, closes := 0, 0
	closed := false
	for _, event := range events {
		if err := integrationpkg.ValidateTraceRecordAt(event, time.Time{}); err != nil {
			return firstFrameCallbackCounts{}, err
		}
		if event.Session != session {
			return firstFrameCallbackCounts{}, errors.New("first-frame callback trace contains multiple sessions")
		}
		stamp, err := time.Parse(time.RFC3339Nano, event.Time)
		if err != nil {
			return firstFrameCallbackCounts{}, err
		}
		if !previous.IsZero() && stamp.Before(previous) {
			return firstFrameCallbackCounts{}, errors.New("first-frame callback trace timestamps decrease")
		}
		if closed && event.Event != "session.close" {
			return firstFrameCallbackCounts{}, errors.New("first-frame callback trace has an event after session.close")
		}
		previous = stamp
		include := through.IsZero() || !stamp.After(through)
		switch event.Event {
		case "session.start":
			starts++
		case "session.close":
			closes++
			closed = true
		case "callback.info":
			if event.Generation != 0 || event.Outcome != "ok" && event.Outcome != "error" {
				return firstFrameCallbackCounts{}, errors.New("invalid callback.info invocation")
			}
		case "callback.info.start":
			if event.Generation != 0 || event.Renderer != "" || event.Outcome != "started" {
				return firstFrameCallbackCounts{}, errors.New("invalid callback.info start")
			}
			if include {
				counts.info++
			}
		case "callback.display":
			if event.Generation != 0 || event.Renderer != "" || event.Outcome != "ok" && event.Outcome != "error" {
				return firstFrameCallbackCounts{}, errors.New("invalid callback.display invocation")
			}
		case "callback.display.start":
			if event.Generation != 0 || event.Renderer != "" || event.Outcome != "started" {
				return firstFrameCallbackCounts{}, errors.New("invalid callback.display start")
			}
			if include {
				counts.display++
			}
		case "callback.preview":
			if event.Generation != 0 || event.Renderer != "" || event.Outcome != "ok" && event.Outcome != "error" {
				return firstFrameCallbackCounts{}, errors.New("invalid callback.preview invocation")
			}
		case "callback.preview.start":
			if event.Generation != 0 || event.Renderer != "" || event.Outcome != "started" {
				return firstFrameCallbackCounts{}, errors.New("invalid callback.preview start")
			}
			if include {
				counts.preview++
			}
		case "callback.event":
			if event.Generation != 0 || event.Renderer != "" || event.Outcome == "started" {
				return firstFrameCallbackCounts{}, errors.New("invalid callback.event invocation")
			}
		case "callback.event.start":
			if event.Generation != 0 || event.Renderer != "" || event.Outcome != "started" {
				return firstFrameCallbackCounts{}, errors.New("invalid callback.event start")
			}
			if include {
				counts.event++
			}
		case "callback.load":
			if event.Generation == 0 || event.Renderer != "" || event.Outcome != "ok" && event.Outcome != "error" {
				return firstFrameCallbackCounts{}, errors.New("invalid callback.load invocation")
			}
		case "callback.load.start":
			if event.Generation == 0 || event.Renderer != "" || event.Outcome != "started" {
				return firstFrameCallbackCounts{}, errors.New("invalid callback.load start")
			}
			if include {
				counts.load++
			}
		}
	}
	if starts != 1 || (requireTerminal && closes != 1) || (!requireTerminal && closes > 1) {
		return firstFrameCallbackCounts{}, fmt.Errorf("first-frame callback lifecycle starts=%d closes=%d", starts, closes)
	}
	return counts, nil
}

func firstFrameRendererCategory(command string) string {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return ""
	}
	field := strings.Trim(fields[0], "\"'")
	field = strings.ToLower(field)
	if strings.HasSuffix(field, ".exe") {
		field = strings.TrimSuffix(field, ".exe")
	}
	for _, renderer := range []string{"eza", "bat", "glow", "kitten", "chafa", "unzip", "gzip", "xz", "tar", "file"} {
		if strings.HasSuffix(field, renderer) || strings.Contains(field, "\\"+renderer) || strings.Contains(field, "/"+renderer) {
			return renderer
		}
	}
	return ""
}

func firstFrameCallbackCategory(command string) (string, error) {
	if strings.Contains(command, "--with-shell=") {
		return "", errors.New("fzf launcher is not a callback")
	}
	index := strings.Index(command, "--fzf-shell")
	if index < 0 {
		return "", errors.New("callback flag is missing")
	}
	parts := strings.Fields(strings.TrimSpace(command[index+len("--fzf-shell"):]))
	if len(parts) == 0 {
		return "", errors.New("callback command is missing")
	}
	commandName := strings.Trim(parts[0], "\"'")
	switch {
	case commandName == "d":
		return "display", nil
	case commandName == "p" || commandName == "p:invalid":
		return "preview", nil
	case strings.HasPrefix(commandName, "i:") && (commandName == "i:cd" || commandName == "i:cp"):
		return "info", nil
	case strings.HasPrefix(commandName, "e:"):
		return "event", nil
	case strings.HasPrefix(commandName, "l:"):
		return "load", nil
	default:
		return "", fmt.Errorf("unknown callback %q", commandName)
	}
}
