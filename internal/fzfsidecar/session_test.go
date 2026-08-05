package fzfsidecar

import (
	"bytes"
	"encoding/base64"
	"errors"
	"net"
	"regexp"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestNewRejectsUnknownPicker(t *testing.T) {
	if _, err := New(protocol.Picker("unknown")); err == nil {
		t.Fatal("New accepted an unknown picker")
	}
}

func TestNewProvidesNumericLoopbackAddressAndIndependentKeys(t *testing.T) {
	first, err := New(protocol.PickerCD)
	if err != nil {
		t.Fatalf("New first: %v", err)
	}
	second, err := New(protocol.PickerCP)
	if err != nil {
		t.Fatalf("New second: %v", err)
	}

	addressPattern := regexp.MustCompile(`^127\.0\.0\.1:[1-9][0-9]*$`)
	for name, address := range map[string]string{
		"first":  first.Address(),
		"second": second.Address(),
	} {
		if !addressPattern.MatchString(address) {
			t.Errorf("%s address = %q, want numeric IPv4 loopback", name, address)
		}
		if _, _, err := net.SplitHostPort(address); err != nil {
			t.Errorf("%s address is not host:port: %v", name, err)
		}
	}
	if first.APIKey() == second.APIKey() {
		t.Fatal("independent sessions received the same API key")
	}
	for name, key := range map[string]string{"first": first.APIKey(), "second": second.APIKey()} {
		decoded, err := base64.RawURLEncoding.Strict().DecodeString(key)
		if err != nil || len(decoded) != 32 {
			t.Errorf("%s API key = %q, want strict raw URL-safe base64 of 32 bytes", name, key)
		}
	}
}

func TestNewClosesInjectedReservationExactlyOnce(t *testing.T) {
	reservation, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve test port: %v", err)
	}
	wrapped := &countingListener{Listener: reservation}
	var calls int

	session, err := New(protocol.PickerCD,
		WithPortReservation(func(string, string) (net.Listener, error) {
			calls++
			return wrapped, nil
		}),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if calls != 1 {
		t.Fatalf("reservation calls = %d, want 1", calls)
	}
	if wrapped.closeCalls != 1 {
		t.Fatalf("reservation close calls = %d, want 1", wrapped.closeCalls)
	}
	if session.Address() == "" {
		t.Fatal("New returned an empty address")
	}
}

func TestNewUsesInjectedKeyBytesAndDoesNotRetryATakenPort(t *testing.T) {
	reservation, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve test port: %v", err)
	}
	wrapper := &countingListener{Listener: reservation}
	var reservations int
	keyBytes := bytes.Repeat([]byte{0xA5}, 32)
	session, err := New(protocol.PickerCP,
		WithRandomSource(bytes.NewReader(keyBytes)),
		WithPortReservation(func(string, string) (net.Listener, error) {
			reservations++
			return wrapper, nil
		}),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	taken, err := net.Listen("tcp4", session.Address())
	if err != nil {
		t.Fatalf("take reserved port: %v", err)
	}
	t.Cleanup(func() { _ = taken.Close() })
	if reservations != 1 {
		t.Fatalf("reservation calls = %d, want 1", reservations)
	}
	wantKey := base64.RawURLEncoding.EncodeToString(keyBytes)
	if session.APIKey() != wantKey {
		t.Fatalf("API key = %q, want %q", session.APIKey(), wantKey)
	}
}

func TestNewAcceptsANumericLoopbackReservationAddress(t *testing.T) {
	reservation, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve test port: %v", err)
	}
	address := reservation.Addr().String()
	wrapped := &stringAddressListener{Listener: reservation, address: address}
	session, err := New(protocol.PickerCD, WithPortReservation(func(string, string) (net.Listener, error) {
		return wrapped, nil
	}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if session.Address() != address {
		t.Fatalf("Address() = %q, want %q", session.Address(), address)
	}
}

func TestNewClosesReservationWhenKeyGenerationFails(t *testing.T) {
	reservation, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve test port: %v", err)
	}
	wrapped := &countingListener{Listener: reservation}
	if _, err := New(protocol.PickerCD,
		WithRandomSource(errorReader{}),
		WithPortReservation(func(string, string) (net.Listener, error) { return wrapped, nil }),
	); err == nil {
		t.Fatal("New succeeded with a failing random source")
	}
	if wrapped.closeCalls != 1 {
		t.Fatalf("reservation close calls = %d, want 1", wrapped.closeCalls)
	}
}

func TestNewReturnsReservationCloseErrorWithoutSession(t *testing.T) {
	reservation, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve test port: %v", err)
	}
	closeErr := errors.New("reservation close failed")
	wrapped := &closeErrorListener{Listener: reservation, err: closeErr}
	session, err := New(protocol.PickerCD, WithPortReservation(func(string, string) (net.Listener, error) {
		return wrapped, nil
	}))
	if session != nil {
		t.Fatal("New returned a Session when reservation close failed")
	}
	if !errors.Is(err, closeErr) {
		t.Fatalf("New error = %v, want %v", err, closeErr)
	}
	if got := wrapped.closeCalls; got != 1 {
		t.Fatalf("reservation close calls = %d, want 1", got)
	}
}

func TestNewJoinsReservationCloseErrorWithConstructionError(t *testing.T) {
	reservation, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve test port: %v", err)
	}
	constructionErr := errors.New("random source failed")
	closeErr := errors.New("reservation close failed")
	wrapped := &closeErrorListener{Listener: reservation, err: closeErr}

	session, err := New(protocol.PickerCD,
		WithRandomSource(errorReaderWithError{err: constructionErr}),
		WithPortReservation(func(string, string) (net.Listener, error) { return wrapped, nil }),
	)
	if session != nil {
		t.Fatal("New returned a Session after construction failed")
	}
	if !errors.Is(err, constructionErr) {
		t.Fatalf("New error = %v, want construction error %v", err, constructionErr)
	}
	if !errors.Is(err, closeErr) {
		t.Fatalf("New error = %v, want reservation close error %v", err, closeErr)
	}
}

type countingListener struct {
	net.Listener
	closeCalls int
}

type closeErrorListener struct {
	net.Listener
	err        error
	closeCalls int
}

func (listener *closeErrorListener) Close() error {
	listener.closeCalls++
	_ = listener.Listener.Close()
	return listener.err
}

type stringAddressListener struct {
	net.Listener
	address string
}

func (listener *stringAddressListener) Addr() net.Addr { return stringAddr(listener.address) }

type stringAddr string

func (address stringAddr) Network() string { return "tcp4" }

func (address stringAddr) String() string { return string(address) }

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errors.New("random source failed") }

type errorReaderWithError struct{ err error }

func (reader errorReaderWithError) Read([]byte) (int, error) { return 0, reader.err }

func (listener *countingListener) Close() error {
	listener.closeCalls++
	return listener.Listener.Close()
}
