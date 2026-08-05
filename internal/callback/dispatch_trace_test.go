package callback

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	integrationpkg "github.com/AntoineGS/shell-picker/internal/integration"
)

func TestInfoCallbackReportsStartAndSuccessfulCompletionWithoutSensitiveFields(t *testing.T) {
	var got []integrationpkg.TraceEvent
	var stdout bytes.Buffer
	deps := Dependencies{
		LookupEnv: func(key string) string {
			return map[string]string{"FZF_QUERY": "query-secret", "FZF_CURRENT_ITEM": "current-secret", "FZF_KEY": "key-secret", "FZF_MATCH_COUNT": "1", "FZF_TOTAL_COUNT": "2", "FZF_SELECT_COUNT": "1"}[key]
		},
		Stdout: &stdout, Stderr: io.Discard,
		Trace: func(event integrationpkg.TraceEvent) error {
			got = append(got, event)
			return nil
		},
	}
	if err := Dispatch(context.Background(), mustParse(t, "i:cd"), deps); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "callback.info.start" || got[0].Outcome != "started" ||
		got[1].Name != "callback.info" || got[1].Outcome != "ok" {
		t.Fatalf("trace events=%+v", got)
	}
	if stdout.String() == "" || strings.Contains(stdout.String(), "secret") {
		t.Fatalf("info output=%q", stdout.String())
	}
}

func TestInfoCallbackReportsErrorCompletionWithoutErrorText(t *testing.T) {
	var got []integrationpkg.TraceEvent
	deps := Dependencies{
		LookupEnv: func(key string) string {
			return map[string]string{"FZF_MATCH_COUNT": "1", "FZF_TOTAL_COUNT": "2", "FZF_SELECT_COUNT": "1"}[key]
		},
		Stdout: failingWriter{err: errors.New("secret callback output failure")},
		Stderr: io.Discard,
		Trace: func(event integrationpkg.TraceEvent) error {
			got = append(got, event)
			return nil
		},
	}
	err := Dispatch(context.Background(), mustParse(t, "i:cp"), deps)
	if err == nil || !strings.Contains(err.Error(), "secret callback output failure") {
		t.Fatalf("Dispatch error=%v", err)
	}
	if len(got) != 2 || got[0].Name != "callback.info.start" || got[0].Outcome != "started" ||
		got[1].Name != "callback.info" || got[1].Outcome != "error" {
		t.Fatalf("trace events=%+v", got)
	}
	if strings.Contains(got[1].Outcome, "secret") {
		t.Fatalf("error text leaked into trace outcome=%q", got[1].Outcome)
	}
}

type failingWriter struct{ err error }

func (writer failingWriter) Write([]byte) (int, error) { return 0, writer.err }
