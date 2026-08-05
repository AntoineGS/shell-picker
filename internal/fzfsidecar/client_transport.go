package fzfsidecar

import (
	"context"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/AntoineGS/shell-picker/internal/protocol"
)

type client struct {
	address     string
	apiKey      string
	picker      protocol.Picker
	httpClient  *http.Client
	idleCloser  idleConnectionCloser
	activeMu    sync.Mutex
	activeBody  map[*closeOnceBody]struct{}
	idleOnce    sync.Once
	withTimeout func(context.Context, time.Duration) (context.Context, context.CancelFunc)
}

func newHTTPClient(options sessionOptions) (*http.Client, idleConnectionCloser) {
	transport := options.transport
	if transport == nil {
		dialContext := options.dialContext
		if dialContext == nil {
			dialContext = (&net.Dialer{Timeout: dialTimeout}).DialContext
		}
		transport = &http.Transport{
			Proxy:               nil,
			DialContext:         dialContext,
			MaxIdleConns:        2,
			MaxIdleConnsPerHost: 2,
			IdleConnTimeout:     5 * time.Second,
			// fzf may close listener-side idle connections between requests.
			DisableKeepAlives:     true,
			DisableCompression:    false,
			ForceAttemptHTTP2:     false,
			TLSHandshakeTimeout:   dialTimeout,
			ResponseHeaderTimeout: requestTimeout,
			ExpectContinueTimeout: dialTimeout,
		}
	}

	var idleCloser idleConnectionCloser
	if transport, ok := transport.(*http.Transport); ok {
		transport = transport.Clone()
		transport.Proxy = nil
		// Keep the fzf listener transport from reusing a connection it closed.
		transport.DisableKeepAlives = true
		if options.dialContext != nil {
			transport.DialContext = options.dialContext
		}
		idleCloser = transport
		return &http.Client{
			Transport:     transport,
			Timeout:       requestTimeout,
			CheckRedirect: rejectRedirect,
		}, idleCloser
	}
	if closer, ok := transport.(idleConnectionCloser); ok {
		idleCloser = closer
	}
	return &http.Client{
		Transport:     transport,
		Timeout:       requestTimeout,
		CheckRedirect: rejectRedirect,
	}, idleCloser
}

func rejectRedirect(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}

func newClient(address, apiKey string, httpClient *http.Client, idleCloser idleConnectionCloser, picker protocol.Picker) *client {
	return &client{
		address:     address,
		apiKey:      apiKey,
		picker:      picker,
		httpClient:  httpClient,
		idleCloser:  idleCloser,
		activeBody:  make(map[*closeOnceBody]struct{}),
		withTimeout: context.WithTimeout,
	}
}

func (client *client) closeIdleConnections() {
	if client == nil {
		return
	}
	client.activeMu.Lock()
	active := make([]*closeOnceBody, 0, len(client.activeBody))
	for body := range client.activeBody {
		active = append(active, body)
	}
	client.activeMu.Unlock()
	for _, body := range active {
		_ = body.Close()
	}
	client.idleOnce.Do(func() {
		if client.idleCloser != nil {
			client.idleCloser.CloseIdleConnections()
		}
	})
}
