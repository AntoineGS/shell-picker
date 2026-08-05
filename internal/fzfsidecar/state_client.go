package fzfsidecar

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/AntoineGS/shell-picker/internal/finderinfo"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

const (
	selectedPageSize = 100
	// maxSelectedPages bounds one CP selected-items snapshot to 10,000 items.
	// A full page at this cap is rejected because another page may exist.
	maxSelectedPages = 100
	// selectedPaginationTimeout bounds a complete selected-items snapshot
	// independently of the 500ms timeout applied to each HTTP request.
	selectedPaginationTimeout = 2 * time.Second
)

func (client *client) getState(ctx context.Context) (fzfState, error) {
	state, _, err := client.getStateResult(ctx)
	return state, err
}

func (client *client) getStateResult(ctx context.Context) (fzfState, operationDiagnostic, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	withTimeout := client.withTimeout
	if withTimeout == nil {
		withTimeout = context.WithTimeout
	}
	cycleContext, cancel := withTimeout(ctx, selectedPaginationTimeout)
	defer cancel()

	page, diagnostic, err := client.getPageResult(cycleContext, 0)
	if err != nil {
		if ctx.Err() != nil {
			diagnostic := operationDiagnostic{reason: ObserverReasonContextCanceled}
			return fzfState{}, diagnostic, wrapOperationError(ctx.Err(), diagnostic)
		}
		return fzfState{}, diagnostic, err
	}
	if cycleContext.Err() != nil {
		if ctx.Err() != nil {
			diagnostic := operationDiagnostic{reason: ObserverReasonContextCanceled}
			return fzfState{}, diagnostic, wrapOperationError(ctx.Err(), diagnostic)
		}
		diagnostic.reason = ObserverReasonContextCanceled
		return fzfState{}, diagnostic, wrapOperationError(cycleContext.Err(), diagnostic)
	}
	if page.selected > selectedPageSize {
		diagnostic.reason = ObserverReasonInvalidState
		return fzfState{}, diagnostic, wrapOperationError(errInvalidState, diagnostic)
	}
	if _, err := newFZFState(protocol.PickerCP, page.matched, page.total, page.selected); err != nil {
		diagnostic.reason = ObserverReasonInvalidState
		return fzfState{}, diagnostic, wrapOperationError(err, diagnostic)
	}
	matched, total := page.matched, page.total
	if client.picker == protocol.PickerCD {
		state, err := newFZFState(client.picker, matched, total, 0)
		if err != nil {
			diagnostic.reason = ObserverReasonInvalidState
			return fzfState{}, diagnostic, wrapOperationError(err, diagnostic)
		}
		return state, diagnostic, nil
	}

	selected := page.selected
	offset := page.selected
	pageCount := 1
	for page.selected == selectedPageSize {
		if pageCount >= maxSelectedPages {
			diagnostic.reason = ObserverReasonInvalidState
			return fzfState{}, diagnostic, wrapOperationError(errStateTooLarge, diagnostic)
		}
		if cycleContext.Err() != nil {
			diagnostic.reason = ObserverReasonContextCanceled
			return fzfState{}, diagnostic, wrapOperationError(cycleContext.Err(), diagnostic)
		}
		page, diagnostic, err = client.getPageResult(cycleContext, offset)
		if err != nil {
			return fzfState{}, diagnostic, err
		}
		pageCount++
		if page.matched != matched || page.total != total {
			diagnostic.reason = ObserverReasonInconsistentSnapshot
			return fzfState{}, diagnostic, wrapOperationError(errInconsistentSnapshot, diagnostic)
		}
		if page.selected > selectedPageSize {
			diagnostic.reason = ObserverReasonInvalidState
			return fzfState{}, diagnostic, wrapOperationError(errInvalidState, diagnostic)
		}
		if selected > finderinfo.MaxCount-page.selected {
			diagnostic.reason = ObserverReasonInvalidState
			return fzfState{}, diagnostic, wrapOperationError(errInvalidState, diagnostic)
		}
		selected += page.selected
		offset += page.selected
		if _, err := newFZFState(protocol.PickerCP, matched, total, selected); err != nil {
			diagnostic.reason = ObserverReasonInvalidState
			return fzfState{}, diagnostic, wrapOperationError(err, diagnostic)
		}
	}
	state, err := newFZFState(client.picker, matched, total, selected)
	if err != nil {
		diagnostic.reason = ObserverReasonInvalidState
		return fzfState{}, diagnostic, wrapOperationError(err, diagnostic)
	}
	return state, diagnostic, nil
}

func (client *client) getPage(ctx context.Context, offset int) (fzfPage, error) {
	page, _, err := client.getPageResult(ctx, offset)
	return page, err
}

func (client *client) getPageResult(ctx context.Context, offset int) (fzfPage, operationDiagnostic, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if offset < 0 {
		diagnostic := operationDiagnostic{reason: ObserverReasonInvalidState}
		return fzfPage{}, diagnostic, wrapOperationError(errInvalidState, diagnostic)
	}
	requestContext, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, fmt.Sprintf("http://%s/?limit=%d&offset=%d", client.address, selectedPageSize, offset), nil)
	if err != nil {
		diagnostic := operationDiagnostic{reason: ObserverReasonResponse}
		return fzfPage{}, diagnostic, wrapOperationError(errResponse, diagnostic)
	}
	request.Header.Set("X-Api-Key", client.apiKey)
	response, err := client.httpClient.Do(request)
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		if classified := requestContextError(ctx, requestContext); classified != nil {
			diagnostic := operationDiagnostic{reason: ObserverReasonContextCanceled}
			return fzfPage{}, diagnostic, wrapOperationError(classified, diagnostic)
		}
		diagnostic := operationDiagnostic{reason: ObserverReasonTransport}
		return fzfPage{}, diagnostic, wrapOperationError(&transportError{cause: err}, diagnostic)
	}
	body, err := client.readBodyContext(requestContext, response)
	if err != nil {
		if errors.Is(err, errResponseTooBig) {
			diagnostic := diagnosticForError(err)
			diagnostic.status = responseStatus(response)
			return fzfPage{}, diagnostic, wrapOperationError(err, diagnostic)
		}
		if classified := requestContextError(ctx, requestContext); classified != nil {
			diagnostic := operationDiagnostic{status: responseStatus(response), reason: ObserverReasonContextCanceled}
			return fzfPage{}, diagnostic, wrapOperationError(classified, diagnostic)
		}
		diagnostic := diagnosticForError(err)
		diagnostic.status = responseStatus(response)
		return fzfPage{}, diagnostic, wrapOperationError(err, diagnostic)
	}
	if classified := requestContextError(ctx, requestContext); classified != nil {
		diagnostic := operationDiagnostic{status: responseStatus(response), reason: ObserverReasonContextCanceled}
		return fzfPage{}, diagnostic, wrapOperationError(classified, diagnostic)
	}
	if response.StatusCode == http.StatusServiceUnavailable {
		diagnostic := operationDiagnostic{status: response.StatusCode, reason: ObserverReasonHTTPStatus}
		return fzfPage{}, diagnostic, wrapOperationError(&transientCycleError{cause: errInvalidStatus}, diagnostic)
	}
	if response.StatusCode != http.StatusOK {
		diagnostic := operationDiagnostic{status: response.StatusCode, reason: ObserverReasonHTTPStatus}
		return fzfPage{}, diagnostic, wrapOperationError(errInvalidStatus, diagnostic)
	}
	if !isJSON(response.Header.Get("Content-Type")) {
		diagnostic := operationDiagnostic{status: response.StatusCode, reason: ObserverReasonInvalidMIME}
		return fzfPage{}, diagnostic, wrapOperationError(errInvalidMimeType, diagnostic)
	}
	page, err := decodePage(body)
	diagnostic := operationDiagnostic{status: response.StatusCode, reason: ObserverReasonHTTPStatus}
	if err != nil {
		diagnostic.reason = diagnosticForError(err).reason
		return fzfPage{}, diagnostic, wrapOperationError(err, diagnostic)
	}
	return page, diagnostic, nil
}
