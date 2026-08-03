package fzf

import (
	"errors"
	"io"
	"testing"
	"time"
)

func TestInputStreamCopiesInitialDataAndAppends(t *testing.T) {
	initial := []byte("initial")
	stream := NewInputStream(initial)
	initial[0] = 'X'

	appended := []byte(" appended")
	if err := stream.Append(appended); err != nil {
		t.Fatal(err)
	}
	appended[0] = 'X'
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}

	got, err := io.ReadAll(stream)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if string(got) != "initial appended" {
		t.Fatalf("ReadAll() = %q", got)
	}
}

func TestInputStreamReadWakesWhenDataIsAppended(t *testing.T) {
	stream := NewInputStream(nil)
	result := make(chan struct {
		n    int
		data byte
		err  error
	}, 1)
	go func() {
		buffer := make([]byte, 1)
		n, err := stream.Read(buffer)
		result <- struct {
			n    int
			data byte
			err  error
		}{n: n, data: buffer[0], err: err}
	}()

	select {
	case got := <-result:
		t.Fatalf("Read returned before Append: %+v", got)
	case <-time.After(50 * time.Millisecond):
	}

	if err := stream.Append([]byte("x")); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-result:
		if got.n != 1 || got.data != 'x' || got.err != nil {
			t.Fatalf("Read() = %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("Read did not wake after Append")
	}
	_ = stream.Close()
}

func TestInputStreamCloseDrainsBufferedDataThenReturnsEOF(t *testing.T) {
	stream := NewInputStream([]byte("abc"))
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}

	buffer := make([]byte, 2)
	n, err := stream.Read(buffer)
	if n != 2 || string(buffer) != "ab" || err != nil {
		t.Fatalf("first Read() = (%d, %v), data %q", n, err, buffer)
	}
	n, err = stream.Read(buffer)
	if n != 1 || buffer[0] != 'c' || err != nil {
		t.Fatalf("second Read() = (%d, %v), data %q", n, err, buffer)
	}
	n, err = stream.Read(buffer)
	if n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("final Read() = (%d, %v), want EOF", n, err)
	}
}

func TestInputStreamCloseWithErrorDrainsBufferedDataThenReturnsCause(t *testing.T) {
	cause := errors.New("input failed")
	stream := NewInputStream([]byte("data"))
	if err := stream.CloseWithError(cause); err != nil {
		t.Fatal(err)
	}

	got, err := io.ReadAll(stream)
	if string(got) != "data" || !errors.Is(err, cause) {
		t.Fatalf("ReadAll() = (%q, %v), want cause", got, err)
	}
}

func TestInputStreamReadWakesWhenClosedWithError(t *testing.T) {
	cause := errors.New("input failed")
	stream := NewInputStream(nil)
	result := make(chan error, 1)
	go func() {
		_, err := stream.Read(make([]byte, 1))
		result <- err
	}()

	select {
	case err := <-result:
		t.Fatalf("Read returned before CloseWithError: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	if err := stream.CloseWithError(cause); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, cause) {
			t.Fatalf("Read() error = %v, want %v", err, cause)
		}
	case <-time.After(time.Second):
		t.Fatal("Read did not wake after CloseWithError")
	}
}

func TestInputStreamAppendAfterCloseAndRepeatedClose(t *testing.T) {
	stream := NewInputStream(nil)
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("repeated Close() error = %v", err)
	}
	if err := stream.CloseWithError(errors.New("ignored")); err != nil {
		t.Fatalf("CloseWithError after Close() error = %v", err)
	}
	if err := stream.Append([]byte("late")); !errors.Is(err, ErrInputClosed) {
		t.Fatalf("Append() error = %v, want ErrInputClosed", err)
	}
}

func TestInputStreamZeroLengthReadDoesNotConsumeOrBlock(t *testing.T) {
	stream := NewInputStream(nil)
	result := make(chan struct {
		n   int
		err error
	}, 1)
	go func() {
		n, err := stream.Read(nil)
		result <- struct {
			n   int
			err error
		}{n: n, err: err}
	}()

	select {
	case got := <-result:
		if got.n != 0 || got.err != nil {
			t.Fatalf("zero-length Read() = %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("zero-length Read blocked")
	}

	if err := stream.Append([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(stream)
	if err != nil || string(got) != "x" {
		t.Fatalf("ReadAll() = (%q, %v), want x", got, err)
	}
}

func TestInputStreamAppendAfterPublishesOnlyAfterCallbackSucceeds(t *testing.T) {
	stream := NewInputStream(nil)
	publishStarted := make(chan struct{})
	releasePublish := make(chan struct{})
	appendDone := make(chan error, 1)
	go func() {
		appendDone <- stream.AppendAfter([]byte("published"), func() error {
			close(publishStarted)
			<-releasePublish
			return nil
		})
	}()
	<-publishStarted

	readDone := make(chan struct {
		data []byte
		err  error
	}, 1)
	go func() {
		data, err := io.ReadAll(stream)
		readDone <- struct {
			data []byte
			err  error
		}{data: data, err: err}
	}()
	select {
	case result := <-readDone:
		t.Fatalf("reader completed before publication: %+v", result)
	default:
	}
	close(releasePublish)
	if err := <-appendDone; err != nil {
		t.Fatalf("AppendAfter: %v", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	result := <-readDone
	if result.err != nil || string(result.data) != "published" {
		t.Fatalf("reader=(%q, %v), want published data", result.data, result.err)
	}
}

func TestInputStreamAppendAfterFailureDoesNotPublish(t *testing.T) {
	stream := NewInputStream(nil)
	publishErr := errors.New("publish failed")
	if err := stream.AppendAfter([]byte("discarded"), func() error { return publishErr }); !errors.Is(err, publishErr) {
		t.Fatalf("AppendAfter error=%v, want %v", err, publishErr)
	}
	if err := stream.Append([]byte("retained")); err != nil {
		t.Fatal(err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(stream)
	if err != nil || string(data) != "retained" {
		t.Fatalf("ReadAll=(%q, %v), want retained only", data, err)
	}
}

func TestInputStreamAppendAfterClosedDoesNotCallCallback(t *testing.T) {
	stream := NewInputStream(nil)
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	called := false
	if err := stream.AppendAfter([]byte("late"), func() error {
		called = true
		return nil
	}); !errors.Is(err, ErrInputClosed) {
		t.Fatalf("AppendAfter error=%v, want ErrInputClosed", err)
	}
	if called {
		t.Fatal("AppendAfter called publication callback after close")
	}
}

func TestInputStreamAppendAfterRejectsNilCallback(t *testing.T) {
	stream := NewInputStream(nil)
	if err := stream.AppendAfter([]byte("data"), nil); !errors.Is(err, ErrNilPublication) {
		t.Fatalf("AppendAfter error=%v, want ErrNilPublication", err)
	}
	if err := stream.Append([]byte("retained")); err != nil {
		t.Fatal(err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(stream)
	if err != nil || string(data) != "retained" {
		t.Fatalf("ReadAll=(%q, %v), want retained only", data, err)
	}
}

func TestInputStreamCloseWaitsBehindAppendAfterPublication(t *testing.T) {
	stream := NewInputStream(nil)
	publishStarted := make(chan struct{})
	releasePublish := make(chan struct{})
	appendDone := make(chan error, 1)
	go func() {
		appendDone <- stream.AppendAfter([]byte("data"), func() error {
			close(publishStarted)
			<-releasePublish
			return nil
		})
	}()
	<-publishStarted
	closeDone := make(chan error, 1)
	go func() { closeDone <- stream.Close() }()
	select {
	case err := <-closeDone:
		t.Fatalf("Close completed during publication: %v", err)
	default:
	}
	close(releasePublish)
	if err := <-appendDone; err != nil {
		t.Fatalf("AppendAfter: %v", err)
	}
	if err := <-closeDone; err != nil {
		t.Fatalf("Close: %v", err)
	}
	data, err := io.ReadAll(stream)
	if err != nil || string(data) != "data" {
		t.Fatalf("ReadAll=(%q, %v), want data", data, err)
	}
}
