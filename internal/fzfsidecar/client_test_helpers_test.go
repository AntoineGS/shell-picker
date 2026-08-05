package fzfsidecar

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func newHTTPClientForTest(transport http.RoundTripper) *http.Client {
	return &http.Client{Transport: transport, CheckRedirect: rejectRedirect}
}

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func response(status int, contentType, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{contentType}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

type trackedBody struct {
	reader *bytes.Reader
	closes atomic.Int32
}

func (body *trackedBody) Read(buffer []byte) (int, error) { return body.reader.Read(buffer) }

func (body *trackedBody) Close() error {
	body.closes.Add(1)
	return nil
}

type dumpStatusCall struct {
	limit  int
	offset int
}

type dumpStatusFake struct {
	mu sync.Mutex

	totalCount     int
	matchCount     int
	selectedCount  int
	snapshots      []int
	snapshotIndex  int
	activeSelected int

	mutationOffset   int
	mutationTotal    int
	mutationMatch    int
	mutateFirstCycle bool
	overPageCount    int
	oversizedOffset  int
	cancelAfterFirst bool
	cancel           context.CancelFunc

	calls      []dumpStatusCall
	posts      []string
	getEvents  chan<- dumpStatusCall
	postEvents chan<- string
}

func (fake *dumpStatusFake) RoundTrip(request *http.Request) (*http.Response, error) {
	query := request.URL.Query()
	limit, limitErr := strconv.Atoi(query.Get("limit"))
	offset, offsetErr := strconv.Atoi(query.Get("offset"))

	if request.Method == http.MethodPost {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		fake.mu.Lock()
		fake.posts = append(fake.posts, string(body))
		postEvents := fake.postEvents
		fake.mu.Unlock()
		if postEvents != nil {
			postEvents <- string(body)
		}
		return response(http.StatusOK, "text/plain", "ok"), nil
	}
	if request.Method != http.MethodGet {
		return response(http.StatusBadRequest, "text/plain", "invalid pagination"), nil
	}

	fake.mu.Lock()
	call := dumpStatusCall{limit: limit, offset: offset}
	fake.calls = append(fake.calls, call)
	getEvents := fake.getEvents
	if limitErr != nil || offsetErr != nil {
		fake.mu.Unlock()
		if getEvents != nil {
			getEvents <- call
		}
		return response(http.StatusBadRequest, "text/plain", "invalid pagination"), nil
	}
	if limit != selectedPageSize || offset < 0 {
		fake.mu.Unlock()
		if getEvents != nil {
			getEvents <- call
		}
		return response(http.StatusBadRequest, "text/plain", "invalid pagination"), nil
	}
	if len(fake.snapshots) > 0 && offset == 0 {
		index := fake.snapshotIndex
		if index >= len(fake.snapshots) {
			index = len(fake.snapshots) - 1
		}
		fake.activeSelected = fake.snapshots[index]
		fake.snapshotIndex++
	}
	selectedCount := fake.selectedCount
	if len(fake.snapshots) > 0 {
		selectedCount = fake.activeSelected
	}
	totalCount := fake.totalCount
	matchCount := fake.matchCount
	mutate := offset == fake.mutationOffset && fake.mutationTotal != 0
	if fake.mutateFirstCycle {
		mutate = mutate && fake.snapshotIndex == 1
	}
	if mutate {
		totalCount = fake.mutationTotal
		matchCount = fake.mutationMatch
	}
	pageCount := selectedCount - offset
	if pageCount < 0 {
		pageCount = 0
	}
	if pageCount > limit {
		pageCount = limit
	}
	if offset == 0 && fake.overPageCount != 0 {
		pageCount = fake.overPageCount
	}
	callNumber := len(fake.calls)
	cancel := fake.cancelAfterFirst && callNumber == 1
	cancelFunction := fake.cancel
	fake.mu.Unlock()
	if getEvents != nil {
		getEvents <- call
	}

	if cancel && cancelFunction != nil {
		cancelFunction()
	}
	items := make([]map[string]string, pageCount)
	for index := range items {
		items[index] = map[string]string{"text": fmt.Sprintf("dump-item-%d-secret", offset+index)}
	}
	body, err := json.Marshal(struct {
		TotalCount int                 `json:"totalCount"`
		MatchCount int                 `json:"matchCount"`
		Selected   []map[string]string `json:"selected"`
	}{TotalCount: totalCount, MatchCount: matchCount, Selected: items})
	if err != nil {
		return nil, err
	}
	if offset == fake.oversizedOffset && fake.oversizedOffset != 0 {
		return response(http.StatusOK, "application/json", strings.Repeat("x", maxResponseBytes+1)), nil
	}
	return response(http.StatusOK, "application/json", string(body)), nil
}

func (fake *dumpStatusFake) callsSnapshot() []dumpStatusCall {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return append([]dumpStatusCall(nil), fake.calls...)
}
