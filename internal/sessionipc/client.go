package sessionipc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	maxJSONResponseBytes      = 64 << 10
	maxLoadResponseBytes      = 64 << 20
	maxTelemetryResponseBytes = 1 << 10
)

var (
	ErrUnauthorized    = errors.New("IPC request unauthorized")
	ErrNotFound        = errors.New("IPC resource not found")
	ErrBadRequest      = errors.New("invalid IPC request")
	ErrInvalidLoad     = errors.New("invalid IPC load reservation")
	ErrTooManyRequests = errors.New("too many IPC requests")
	ErrInternal        = errors.New("IPC request failed")
)

type Client struct {
	address   string
	token     Token
	client    *http.Client
	transport *http.Transport
}

func NewClientFromEnv(getenv func(string) string) (*Client, error) {
	if getenv == nil {
		return nil, errors.New("create IPC client: nil environment reader")
	}
	address, err := parseEndpoint(getenv(addressEnvironment))
	if err != nil {
		return nil, errors.New("create IPC client: invalid address")
	}
	token, err := parseToken(getenv(tokenEnvironment))
	if err != nil {
		return nil, errors.New("create IPC client: invalid credential")
	}
	return newClient(address, token), nil
}

func newClient(address string, token Token) *Client {
	transport := &http.Transport{
		Proxy: nil,
		DialContext: (&net.Dialer{
			Timeout: 150 * time.Millisecond,
		}).DialContext,
	}
	return &Client{
		address:   address,
		token:     token,
		transport: transport,
		client: &http.Client{
			Transport: transport,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

func (client *Client) Event(ctx context.Context, request EventRequest) (EventResponse, error) {
	var response EventResponse
	err := client.doJSON(ctx, "/v1/event", request, &response)
	return response, err
}

func (client *Client) FinalizeEvent(ctx context.Context, request EventFinalizeRequest) error {
	if request.EventID == 0 {
		return ErrBadRequest
	}
	return client.finalize(ctx, "/v1/event/finalize", request.EventID, request.Applied)
}

func (client *Client) FinalizeLoad(ctx context.Context, request LoadFinalizeRequest) error {
	if request.EventID == 0 {
		return ErrBadRequest
	}
	return client.finalize(ctx, "/v1/load/finalize", request.EventID, request.Applied)
}

func (client *Client) finalize(ctx context.Context, path string, eventID uint64, applied bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := requestContext(context.WithoutCancel(ctx), 250*time.Millisecond)
	defer cancel()
	response, err := client.do(ctx, path, struct {
		EventID uint64 `json:"event_id"`
		Applied bool   `json:"applied"`
	}{EventID: eventID, Applied: applied})
	if err != nil {
		return err
	}
	if _, err := client.readResponse(response, maxTelemetryResponseBytes); err != nil {
		return err
	}
	return statusError(response.StatusCode)
}

func (client *Client) Display(ctx context.Context) (DisplayResponse, error) {
	var response DisplayResponse
	err := client.doJSON(ctx, "/v1/display", DisplayRequest{}, &response)
	return response, err
}

func (client *Client) Load(ctx context.Context, request LoadRequest) ([]byte, error) {
	ctx, cancel := requestContext(ctx, 10*time.Second)
	defer cancel()
	response, err := client.do(ctx, "/v1/load", request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		if _, err := client.readResponse(response, maxJSONResponseBytes); err != nil {
			return nil, err
		}
		if response.Header.Get("Content-Type") != "application/json" {
			return nil, ErrInternal
		}
		return nil, loadStatusError(response.StatusCode)
	}
	if response.Header.Get("Content-Type") != "application/octet-stream" {
		response.Body.Close()
		return nil, ErrInternal
	}
	return client.readResponse(response, maxLoadResponseBytes)
}

func (client *Client) ResolvePreview(ctx context.Context, request PreviewRequest) (PreviewResponse, error) {
	ctx, cancel := requestContext(ctx, 10*time.Second)
	defer cancel()
	var response PreviewResponse
	err := client.doJSON(ctx, "/v1/preview", request, &response)
	return response, err
}

func (client *Client) RecordPreview(ctx context.Context, request PreviewRequest) error {
	if ctx == nil {
		ctx = context.Background()
	}
	softCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 250*time.Millisecond)
	defer cancel()
	response, err := client.do(softCtx, "/v1/preview", request)
	if err != nil {
		return nil
	}
	_, _ = client.readResponse(response, maxTelemetryResponseBytes)
	return nil
}

func (client *Client) doJSON(ctx context.Context, path string, input, output any) error {
	response, err := client.do(ctx, path, input)
	if err != nil {
		return err
	}
	body, err := client.readResponse(response, maxJSONResponseBytes)
	if err != nil {
		return err
	}
	if err := statusError(response.StatusCode); err != nil {
		return err
	}
	if response.Header.Get("Content-Type") != "application/json" {
		return ErrInternal
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(output); err != nil {
		return ErrInternal
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return ErrInternal
	}
	return nil
}

func (client *Client) readResponse(response *http.Response, limit int64) ([]byte, error) {
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		client.CloseIdleConnections()
		return nil, ErrInternal
	}
	if int64(len(body)) > limit {
		client.CloseIdleConnections()
		return nil, ErrInternal
	}
	return body, nil
}

// CloseIdleConnections closes reusable IPC transport connections and is safe to call repeatedly.
func (client *Client) CloseIdleConnections() {
	if client.transport != nil {
		client.transport.CloseIdleConnections()
		return
	}
	if client.client != nil {
		client.client.CloseIdleConnections()
	}
}

func (client *Client) do(ctx context.Context, path string, input any) (*http.Response, error) {
	if ctx == nil {
		return nil, ErrBadRequest
	}
	body, err := json.Marshal(input)
	if err != nil {
		return nil, ErrBadRequest
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.address+path, bytes.NewReader(body))
	if err != nil {
		return nil, ErrBadRequest
	}
	request.Header.Set("Authorization", "Bearer "+client.token.String())
	request.Header.Set("Content-Type", "application/json")
	response, err := client.client.Do(request)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("%w: transport", ErrInternal)
	}
	return response, nil
}

func parseEndpoint(raw string) (string, error) {
	const prefix = "http://127.0.0.1:"
	if !strings.HasPrefix(raw, prefix) {
		return "", errors.New("invalid IPC endpoint")
	}
	portText := strings.TrimPrefix(raw, prefix)
	if portText == "" || (len(portText) > 1 && portText[0] == '0') {
		return "", errors.New("invalid IPC endpoint")
	}
	for _, char := range []byte(portText) {
		if char < '0' || char > '9' {
			return "", errors.New("invalid IPC endpoint")
		}
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || port == 0 {
		return "", errors.New("invalid IPC endpoint")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || parsed.Port() != portText ||
		parsed.User != nil || parsed.Path != "" || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.String() != raw {
		return "", errors.New("invalid IPC endpoint")
	}
	return raw, nil
}

func statusError(status int) error {
	switch status {
	case http.StatusOK, http.StatusNoContent:
		return nil
	case http.StatusUnauthorized:
		return ErrUnauthorized
	case http.StatusNotFound:
		return ErrNotFound
	case http.StatusBadRequest, http.StatusMethodNotAllowed, http.StatusUnsupportedMediaType:
		return ErrBadRequest
	case http.StatusTooManyRequests:
		return ErrTooManyRequests
	default:
		return ErrInternal
	}
}

func loadStatusError(status int) error {
	if status == http.StatusBadRequest || status == http.StatusNotFound {
		return ErrInvalidLoad
	}
	return statusError(status)
}

func requestContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(ctx, timeout)
}
