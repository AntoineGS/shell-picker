package fzfsidecar

import (
	"bytes"
	"context"
	"net/http"
)

func (client *client) postLabel(ctx context.Context, label string) error {
	state, err := parseFormattedLabel(label)
	if err != nil {
		return errInvalidAction
	}
	return client.postState(ctx, state)
}

func (client *client) postState(ctx context.Context, state fzfState) error {
	_, err := client.postStateResult(ctx, state)
	return err
}

func (client *client) postStateResult(ctx context.Context, state fzfState) (operationDiagnostic, error) {
	action, err := state.actionBody()
	if err != nil {
		return operationDiagnostic{reason: ObserverReasonInvalidAction}, wrapOperationError(err, operationDiagnostic{reason: ObserverReasonInvalidAction})
	}
	return client.postActionResult(ctx, action)
}

func (client *client) postAction(ctx context.Context, action []byte) error {
	_, err := client.postActionResult(ctx, action)
	return err
}

func (client *client) postActionResult(ctx context.Context, action []byte) (operationDiagnostic, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	requestContext, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodPost, "http://"+client.address+"/", bytes.NewReader(action))
	if err != nil {
		diagnostic := operationDiagnostic{reason: ObserverReasonResponse}
		return diagnostic, wrapOperationError(errResponse, diagnostic)
	}
	request.Header.Set("X-Api-Key", client.apiKey)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := client.httpClient.Do(request)
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		if classified := requestContextError(ctx, requestContext); classified != nil {
			diagnostic := operationDiagnostic{reason: ObserverReasonContextCanceled}
			return diagnostic, wrapOperationError(classified, diagnostic)
		}
		diagnostic := operationDiagnostic{reason: ObserverReasonTransport}
		return diagnostic, wrapOperationError(&transportError{cause: err}, diagnostic)
	}
	if _, err := client.readBodyContext(requestContext, response); err != nil {
		if classified := requestContextError(ctx, requestContext); classified != nil {
			diagnostic := operationDiagnostic{status: responseStatus(response), reason: ObserverReasonContextCanceled}
			return diagnostic, wrapOperationError(classified, diagnostic)
		}
		diagnostic := diagnosticForError(err)
		diagnostic.status = responseStatus(response)
		return diagnostic, wrapOperationError(err, diagnostic)
	}
	if classified := requestContextError(ctx, requestContext); classified != nil {
		diagnostic := operationDiagnostic{status: responseStatus(response), reason: ObserverReasonContextCanceled}
		return diagnostic, wrapOperationError(classified, diagnostic)
	}
	if response.StatusCode != http.StatusOK {
		diagnostic := operationDiagnostic{status: response.StatusCode, reason: ObserverReasonHTTPStatus}
		var cause error = errInvalidStatus
		if response.StatusCode == http.StatusServiceUnavailable {
			cause = &transientCycleError{cause: errInvalidStatus}
		}
		return diagnostic, wrapOperationError(cause, diagnostic)
	}
	return operationDiagnostic{status: response.StatusCode, reason: ObserverReasonHTTPStatus}, nil
}

func responseStatus(response *http.Response) int {
	if response == nil {
		return 0
	}
	return response.StatusCode
}

func actionBody(label string) ([]byte, error) {
	state, err := parseFormattedLabel(label)
	if err != nil {
		return nil, errInvalidAction
	}
	return state.actionBody()
}
