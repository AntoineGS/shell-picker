package sessionipc

import (
	"bytes"
	"context"
	"net"
	"sync"
	"sync/atomic"
)

const rawHeaderReadLimit = 12 << 10

type strictListener struct {
	net.Listener
}

func (listener strictListener) Accept() (net.Conn, error) {
	connection, err := listener.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &strictAuthConn{Conn: connection}, nil
}

type strictAuthConn struct {
	net.Conn
	once      sync.Once
	buffer    []byte
	offset    int
	readErr   error
	authValid atomic.Bool
}

func (connection *strictAuthConn) Read(destination []byte) (int, error) {
	connection.once.Do(connection.readHeader)
	if connection.offset < len(connection.buffer) {
		count := copy(destination, connection.buffer[connection.offset:])
		connection.offset += count
		return count, nil
	}
	if connection.readErr != nil {
		err := connection.readErr
		connection.readErr = nil
		return 0, err
	}
	return connection.Conn.Read(destination)
}

func (connection *strictAuthConn) readHeader() {
	buffer := make([]byte, 0, 4096)
	chunk := make([]byte, 4096)
	for bytes.Index(buffer, []byte("\r\n\r\n")) < 0 && len(buffer) <= rawHeaderReadLimit {
		count, err := connection.Conn.Read(chunk)
		buffer = append(buffer, chunk[:count]...)
		if err != nil {
			connection.readErr = err
			break
		}
	}
	connection.buffer = buffer
	separator := bytes.Index(buffer, []byte("\r\n\r\n"))
	if separator < 0 || separator > rawHeaderReadLimit {
		return
	}
	connection.authValid.Store(validRawAuthorization(buffer[:separator]))
}

func validRawAuthorization(header []byte) bool {
	lines := bytes.Split(header, []byte("\r\n"))
	count := 0
	for _, line := range lines[1:] {
		colon := bytes.IndexByte(line, ':')
		if colon < 0 || !bytes.EqualFold(line[:colon], []byte("Authorization")) {
			continue
		}
		count++
		value := line[colon+1:]
		if len(value) != len(" Bearer ")+len(Token{}.String()) || !bytes.HasPrefix(value, []byte(" Bearer ")) ||
			!rawURLToken(value[len(" Bearer "):]) {
			return false
		}
	}
	return count == 1
}

func rawURLToken(token []byte) bool {
	for _, char := range token {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '_' || char == '-' {
			continue
		}
		return false
	}
	return true
}

type rawAuthorizationContextKey struct{}

func withRawAuthorization(ctx context.Context, connection net.Conn) context.Context {
	strict, ok := connection.(*strictAuthConn)
	if !ok {
		return ctx
	}
	return context.WithValue(ctx, rawAuthorizationContextKey{}, &strict.authValid)
}

func rawAuthorizationValid(ctx context.Context) bool {
	valid, ok := ctx.Value(rawAuthorizationContextKey{}).(*atomic.Bool)
	return !ok || valid.Load()
}
