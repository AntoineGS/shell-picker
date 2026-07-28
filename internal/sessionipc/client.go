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

var (
	ErrUnauthorized    = errors.New("IPC request unauthorized")
	ErrNotFound        = errors.New("IPC resource not found")
	ErrBadRequest      = errors.New("invalid IPC request")
	ErrTooManyRequests = errors.New("too many IPC requests")
	ErrInternal        = errors.New("IPC request failed")
)

type Client struct {
	address string
	token   Token
	client  *http.Client
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
		address: address,
		token:   token,
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

func (client *Client) Load(ctx context.Context, request LoadRequest) ([]byte, error) {
	ctx, cancel := requestContext(ctx, 10*time.Second)
	defer cancel()
	response, err := client.do(ctx, "/v1/load", request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if err := statusError(response.StatusCode); err != nil {
		return nil, err
	}
	if response.Header.Get("Content-Type") != "application/octet-stream" {
		return nil, ErrInternal
	}
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, ErrInternal
	}
	return data, nil
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
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	return nil
}

func (client *Client) doJSON(ctx context.Context, path string, input, output any) error {
	response, err := client.do(ctx, path, input)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if err := statusError(response.StatusCode); err != nil {
		return err
	}
	if response.Header.Get("Content-Type") != "application/json" {
		return ErrInternal
	}
	decoder := json.NewDecoder(response.Body)
	if err := decoder.Decode(output); err != nil {
		return ErrInternal
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return ErrInternal
	}
	return nil
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

func requestContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(ctx, timeout)
}
