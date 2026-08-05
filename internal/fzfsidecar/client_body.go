package fzfsidecar

import (
	"context"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"
)

func (client *client) readBody(response *http.Response) ([]byte, error) {
	return client.readBodyContext(context.Background(), response)
}

func (client *client) readBodyContext(ctx context.Context, response *http.Response) ([]byte, error) {
	if response == nil || response.Body == nil {
		return nil, errResponse
	}
	body := &closeOnceBody{body: response.Body}
	client.registerBody(body)
	stopClose := context.AfterFunc(ctx, func() { _ = body.Close() })
	defer stopClose()
	defer client.unregisterBody(body)
	defer func() {
		_ = body.Close()
	}()
	data, err := io.ReadAll(io.LimitReader(body, maxResponseBytes+1))
	if err != nil {
		client.closeIdleConnections()
		return nil, err
	}
	if len(data) > maxResponseBytes {
		client.closeIdleConnections()
		return nil, errResponseTooBig
	}
	return data, nil
}

func (client *client) registerBody(body *closeOnceBody) {
	client.activeMu.Lock()
	if client.activeBody == nil {
		client.activeBody = make(map[*closeOnceBody]struct{})
	}
	client.activeBody[body] = struct{}{}
	client.activeMu.Unlock()
}

func (client *client) unregisterBody(body *closeOnceBody) {
	client.activeMu.Lock()
	delete(client.activeBody, body)
	client.activeMu.Unlock()
}

type closeOnceBody struct {
	body io.ReadCloser
	once sync.Once
	err  error
}

func (body *closeOnceBody) Read(buffer []byte) (int, error) {
	return body.body.Read(buffer)
}

func (body *closeOnceBody) Close() error {
	body.once.Do(func() { body.err = body.body.Close() })
	return body.err
}

func isJSON(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	return err == nil && strings.EqualFold(mediaType, "application/json")
}
