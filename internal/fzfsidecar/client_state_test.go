package fzfsidecar

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/finderinfo"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestDecodeStateAllowsUnknownFieldsAndFormatsPickerCounts(t *testing.T) {
	got, err := decodeState([]byte(`{"totalCount":42,"matchCount":7,"selected":[{"text":"a"},{"text":"b"}],"query":"ignored"}`), protocol.PickerCP)
	if err != nil {
		t.Fatalf("decodeState: %v", err)
	}
	if got.formatted != "7/42 (2)" {
		t.Fatalf("decodeState = %q, want %q", got.formatted, "7/42 (2)")
	}
}

func TestDecodeStateRequiresStrictIntegerCountsAndSelectedArray(t *testing.T) {
	tests := []string{
		`{"totalCount":1.0,"matchCount":1,"selected":[]}`,
		`{"totalCount":"1","matchCount":1,"selected":[]}`,
		`{"totalCount":1,"matchCount":1e0,"selected":[]}`,
		`{"totalCount":1,"matchCount":1,"selected":"not-array"}`,
		`{"totalCount":1,"matchCount":1,"selected":null}`,
		`{"totalCount":1,"matchCount":1,"selected":[]}{}`,
		`{"totalCount":1,"matchCount":2,"selected":[]}`,
		`{"totalCount":1,"matchCount":1,"selected":[{},{}]}`,
	}
	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			if _, err := decodeState([]byte(input), protocol.PickerCD); err == nil {
				t.Fatal("decodeState accepted malformed state")
			}
		})
	}
}

func TestActionBodyUsesOnlyTheValidatedFZFActionGrammar(t *testing.T) {
	tests := []struct {
		label string
		want  string
	}{
		{label: "7/42", want: "change-list-label:7/42"},
		{label: "7/42 (2)", want: "change-list-label:7/42 (2)"},
	}
	for _, test := range tests {
		got, err := actionBody(test.label)
		if err != nil {
			t.Fatalf("actionBody(%q): %v", test.label, err)
		}
		if string(got) != test.want {
			t.Errorf("actionBody(%q) = %q, want %q", test.label, got, test.want)
		}
	}
	invalidLabels := []string{"", "01/42", "7/042", "7/42 (02)", "8/7", "7/7 (0)", "7/7 (8)", "7/7  (1)", "7/7(1)", "7/7\n", "7/42 (x)", "7/42()", "7/42 (2) extra", "change-list-label:7/42"}
	maxCount := strconv.Itoa(finderinfo.MaxCount + 1)
	invalidLabels = append(invalidLabels, maxCount+"/"+maxCount)
	invalidLabels = append(invalidLabels, "0/"+strconv.Itoa(finderinfo.MaxCount)+" ("+maxCount+")")
	for _, label := range invalidLabels {
		if _, err := actionBody(label); err == nil {
			t.Errorf("actionBody(%q) accepted invalid action text", label)
		}
	}
}

func TestPostStateConstructsActionFromTypedCountsNotCachedText(t *testing.T) {
	var gotBody string
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		gotBody = string(body)
		return response(http.StatusOK, "text/plain", "ok"), nil
	})
	client := newClient("127.0.0.1:12345", "secret", &http.Client{Transport: transport}, nil, protocol.PickerCD)

	state := fzfState{picker: protocol.PickerCD, matched: 7, total: 42, selected: 0, formatted: "attacker-controlled"}
	if err := client.postState(context.Background(), state); err != nil {
		t.Fatalf("postState: %v", err)
	}
	if gotBody != "change-list-label:7/42" {
		t.Fatalf("POST body = %q, want %q", gotBody, "change-list-label:7/42")
	}
}

func TestPostStateRejectsInconsistentTypedCountsBeforeHTTP(t *testing.T) {
	var calls atomic.Int32
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return response(http.StatusOK, "text/plain", "ok"), nil
	})
	client := newClient("127.0.0.1:12345", "secret", &http.Client{Transport: transport}, nil, protocol.PickerCD)

	if err := client.postState(context.Background(), fzfState{picker: protocol.PickerCD, matched: 8, total: 7, formatted: "8/7"}); err == nil {
		t.Fatal("postState accepted inconsistent typed counts")
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("HTTP calls = %d, want 0", got)
	}
}

func TestTypedStateValidationRejectsRelationalMismatches(t *testing.T) {
	for _, test := range []struct {
		name           string
		matched, total int
		selected       int
	}{
		{name: "matched exceeds total", matched: 8, total: 7},
		{name: "selected exceeds total", matched: 7, total: 7, selected: 8},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := newFZFState(protocol.PickerCP, test.matched, test.total, test.selected); err == nil {
				t.Fatal("newFZFState accepted relationally inconsistent counts")
			}
		})
	}
}

func TestClientKeepsTypedGetTransportFailuresRaw(t *testing.T) {
	tests := []struct {
		name  string
		cause error
	}{
		{name: "EOF", cause: io.EOF},
		{name: "unexpected EOF", cause: io.ErrUnexpectedEOF},
		{name: "connection reset", cause: syscall.ECONNRESET},
		{name: "connection refused", cause: syscall.ECONNREFUSED},
		{name: "arbitrary URL error", cause: &url.Error{Op: "roundtrip", URL: "http://sidecar.invalid/", Err: errors.New("bounded transport failure")}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, test.cause
			})
			client := newClient("127.0.0.1:12345", "secret", &http.Client{Transport: transport}, nil, protocol.PickerCD)
			_, err := client.getState(context.Background())
			if !errors.Is(err, errTransport) || errors.Is(err, errTransientCycle) || !errors.Is(err, test.cause) {
				t.Fatalf("getState error=%v, want raw typed transport error", err)
			}
		})
	}
}
