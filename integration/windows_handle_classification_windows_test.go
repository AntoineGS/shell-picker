//go:build windows

package integration

import (
	"errors"
	"fmt"
	"net"
	"os"
	"runtime"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func task20KindForObjectType(objectType string) (task20HandleKind, bool) {
	switch objectType {
	case "Process":
		return task20HandleProcess, true
	case "Job":
		return task20HandleJob, true
	case "Thread":
		return task20HandleThread, true
	case "Event":
		return task20HandleEvent, true
	case "Timer":
		return task20HandleTimer, true
	case "IoCompletion":
		return task20HandleIOCompletion, true
	case "WaitCompletionPacket":
		return task20HandleWaitCompletion, true
	default:
		return task20HandleUnknown, false
	}
}

type task20HandleClassificationAPI struct {
	queryObjectType func(windows.Handle) (string, error)
	getsockname     func(windows.Handle) (windows.Sockaddr, error)
	getFileType     func(windows.Handle) (uint32, error)
}

func task20ClassifyHandle(handle windows.Handle, identity task20HandleIdentity) (task20ResourceIdentity, error) {
	return task20ClassifyHandleWith(handle, identity, task20HandleClassificationAPI{
		queryObjectType: task20QueryObjectType,
		getsockname:     windows.Getsockname,
		getFileType:     windows.GetFileType,
	})
}

func task20ClassifyHandleWith(handle windows.Handle, identity task20HandleIdentity, api task20HandleClassificationAPI) (task20ResourceIdentity, error) {
	typeName, err := api.queryObjectType(handle)
	if err != nil {
		return task20ResourceIdentity{}, fmt.Errorf("query handle %#x object type: %w", uintptr(handle), err)
	}
	if typeName == "File" {
		if _, err := api.getsockname(handle); err == nil {
			return task20ResourceIdentity{Identity: identity, Type: typeName, Kind: task20HandleSocket}, nil
		} else if !errors.Is(err, windows.WSAENOTSOCK) {
			return task20ResourceIdentity{}, fmt.Errorf("probe handle %#x as socket: %w", uintptr(handle), err)
		}

		fileType, err := api.getFileType(handle)
		if err != nil {
			return task20ResourceIdentity{}, fmt.Errorf("get handle %#x file type: %w", uintptr(handle), err)
		}
		switch fileType {
		case windows.FILE_TYPE_PIPE:
			return task20ResourceIdentity{Identity: identity, Type: typeName, Kind: task20HandlePipe}, nil
		case windows.FILE_TYPE_DISK, windows.FILE_TYPE_CHAR:
			return task20ResourceIdentity{Identity: identity, Type: typeName, Kind: task20HandleFile}, nil
		default:
			return task20ResourceIdentity{}, fmt.Errorf("handle %#x has unsupported file type %#x", uintptr(handle), fileType)
		}
	}

	kind, ok := task20KindForObjectType(typeName)
	if !ok {
		return task20ResourceIdentity{}, fmt.Errorf("handle %#x has unsupported object type %q", uintptr(handle), typeName)
	}
	return task20ResourceIdentity{Identity: identity, Type: typeName, Kind: kind}, nil
}

func TestTask20KnownObjectTypePolicy(t *testing.T) {
	cases := map[string]task20HandleKind{
		"Process":              task20HandleProcess,
		"Job":                  task20HandleJob,
		"Thread":               task20HandleThread,
		"Event":                task20HandleEvent,
		"Timer":                task20HandleTimer,
		"IoCompletion":         task20HandleIOCompletion,
		"WaitCompletionPacket": task20HandleWaitCompletion,
	}
	for name, want := range cases {
		got, ok := task20KindForObjectType(name)
		if !ok || got != want {
			t.Errorf("type %q kind=%v ok=%v want=%v", name, got, ok, want)
		}
	}
}

func TestTask20UnknownObjectTypeFailsClosed(t *testing.T) {
	if _, ok := task20KindForObjectType("FutureRuntimeObject"); ok {
		t.Fatal("unknown object type was classified")
	}
}

func TestTask20RuntimeObjectKindsAreNotApplicationOwned(t *testing.T) {
	for _, kind := range []task20HandleKind{
		task20HandleThread,
		task20HandleEvent,
		task20HandleTimer,
		task20HandleIOCompletion,
		task20HandleWaitCompletion,
	} {
		owned, err := (task20ResourceIdentity{Kind: kind}).applicationOwned()
		if err != nil || owned {
			t.Errorf("runtime kind=%v was classified as application-owned", kind)
		}
	}
}

func TestTask20UnknownHandleKindsFailClosed(t *testing.T) {
	for _, kind := range []task20HandleKind{task20HandleUnknown, task20HandleKind(255)} {
		owned, err := (task20ResourceIdentity{Kind: kind}).applicationOwned()
		if err == nil || owned {
			t.Errorf("kind=%v owned=%v err=%v; want an ownership error", kind, owned, err)
		}
	}
}

func TestTask20ClassifyHandleRejectsUnknownObjectType(t *testing.T) {
	assertTask20InjectedClassificationError(t, task20HandleClassificationAPI{
		queryObjectType: func(windows.Handle) (string, error) {
			return "FutureRuntimeObject", nil
		},
		getsockname: func(windows.Handle) (windows.Sockaddr, error) {
			t.Fatal("socket probe ran for an unknown object type")
			return nil, nil
		},
		getFileType: func(windows.Handle) (uint32, error) {
			t.Fatal("file type probe ran for an unknown object type")
			return 0, nil
		},
	}, nil, `unsupported object type "FutureRuntimeObject"`)
}

func TestTask20ClassifyHandleRejectsObjectQueryError(t *testing.T) {
	wantErr := errors.New("object query failed")
	assertTask20InjectedClassificationError(t, task20HandleClassificationAPI{
		queryObjectType: func(windows.Handle) (string, error) {
			return "", wantErr
		},
	}, wantErr, "")
}

func TestTask20ClassifyHandleRejectsUnexpectedSocketError(t *testing.T) {
	wantErr := errors.New("socket probe failed")
	fileTypeCalled := false
	assertTask20InjectedClassificationError(t, task20HandleClassificationAPI{
		queryObjectType: func(windows.Handle) (string, error) {
			return "File", nil
		},
		getsockname: func(windows.Handle) (windows.Sockaddr, error) {
			return nil, wantErr
		},
		getFileType: func(windows.Handle) (uint32, error) {
			fileTypeCalled = true
			return windows.FILE_TYPE_PIPE, nil
		},
	}, wantErr, "")
	if fileTypeCalled {
		t.Fatal("file type probe ran after an unexpected socket error")
	}
}

func TestTask20ClassifyHandleRejectsFileTypeError(t *testing.T) {
	wantErr := errors.New("file type query failed")
	assertTask20InjectedClassificationError(t, task20HandleClassificationAPI{
		queryObjectType: func(windows.Handle) (string, error) {
			return "File", nil
		},
		getsockname: func(windows.Handle) (windows.Sockaddr, error) {
			return nil, windows.WSAENOTSOCK
		},
		getFileType: func(windows.Handle) (uint32, error) {
			return 0, wantErr
		},
	}, wantErr, "")
}

func TestTask20ClassifyHandleRejectsUnsupportedFileType(t *testing.T) {
	assertTask20InjectedClassificationError(t, task20HandleClassificationAPI{
		queryObjectType: func(windows.Handle) (string, error) {
			return "File", nil
		},
		getsockname: func(windows.Handle) (windows.Sockaddr, error) {
			return nil, windows.WSAENOTSOCK
		},
		getFileType: func(windows.Handle) (uint32, error) {
			return windows.FILE_TYPE_REMOTE, nil
		},
	}, nil, "unsupported file type")
}

func assertTask20InjectedClassificationError(t *testing.T, api task20HandleClassificationAPI, wantErr error, wantText string) {
	t.Helper()
	got, err := task20ClassifyHandleWith(windows.Handle(0x1234), task20HandleIdentity{Value: 1, Object: 2}, api)
	if err == nil {
		t.Fatal("classification succeeded")
	}
	if wantErr != nil && !errors.Is(err, wantErr) {
		t.Fatalf("error=%v; want wrapped %v", err, wantErr)
	}
	if wantText != "" && !strings.Contains(err.Error(), wantText) {
		t.Fatalf("error=%q; want substring %q", err, wantText)
	}
	if got != (task20ResourceIdentity{}) {
		t.Fatalf("classification=%+v on error; want zero identity", got)
	}
}

func TestTask20NativeHandleClassification(t *testing.T) {
	t.Run("File", func(t *testing.T) {
		file, err := os.CreateTemp(t.TempDir(), "task20-file-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = file.Close() })
		handle := windows.Handle(file.Fd())
		assertTask20NativeHandleClassification(t, handle, task20NativeHandleIdentity(t, handle), "File", task20HandleFile)
	})

	t.Run("Pipe", func(t *testing.T) {
		reader, writer, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = reader.Close() })
		t.Cleanup(func() { _ = writer.Close() })
		readerHandle := windows.Handle(reader.Fd())
		writerHandle := windows.Handle(writer.Fd())
		assertTask20NativeHandleClassification(t, readerHandle, task20NativeHandleIdentity(t, readerHandle), "File", task20HandlePipe)
		assertTask20NativeHandleClassification(t, writerHandle, task20NativeHandleIdentity(t, writerHandle), "File", task20HandlePipe)
	})

	t.Run("Socket", func(t *testing.T) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = listener.Close() })
		tcpListener, ok := listener.(*net.TCPListener)
		if !ok {
			t.Fatalf("listener type=%T; want *net.TCPListener", listener)
		}
		file, err := tcpListener.File()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = file.Close() })
		handle := windows.Handle(file.Fd())
		assertTask20NativeHandleClassification(t, handle, task20NativeHandleIdentity(t, handle), "File", task20HandleSocket)
	})

	t.Run("Process", func(t *testing.T) {
		handle, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, windows.GetCurrentProcessId())
		if err != nil {
			t.Fatal(err)
		}
		if handle == 0 || handle == windows.CurrentProcess() {
			t.Fatalf("OpenProcess returned pseudohandle or zero: %#x", uintptr(handle))
		}
		t.Cleanup(func() {
			if err := windows.CloseHandle(handle); err != nil {
				t.Errorf("close process handle %#x: %v", uintptr(handle), err)
			}
		})
		assertTask20NativeHandleClassification(t, handle, task20NativeHandleIdentity(t, handle), "Process", task20HandleProcess)
	})

	t.Run("Job", func(t *testing.T) {
		job, err := windows.CreateJobObject(nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := windows.CloseHandle(job); err != nil {
				t.Errorf("close job handle %#x: %v", uintptr(job), err)
			}
		})
		assertTask20NativeHandleClassification(t, job, task20NativeHandleIdentity(t, job), "Job", task20HandleJob)
	})
}

func task20NativeHandleIdentity(t *testing.T, handle windows.Handle) task20HandleIdentity {
	t.Helper()
	identities, err := task20CurrentProcessHandleIdentities()
	if err != nil {
		t.Fatalf("snapshot native handle %#x identity: %v", uintptr(handle), err)
	}
	for identity := range identities {
		if identity.Value != uintptr(handle) {
			continue
		}
		if identity.Object == 0 {
			t.Fatalf("native handle %#x has zero object identity", uintptr(handle))
		}
		return identity
	}
	t.Fatalf("native handle %#x was absent from current-process handle snapshot", uintptr(handle))
	return task20HandleIdentity{}
}

func assertTask20NativeHandleClassification(t *testing.T, handle windows.Handle, identity task20HandleIdentity, objectType string, kind task20HandleKind) {
	t.Helper()
	if identity.Value == 0 || identity.Object == 0 {
		t.Fatalf("fixture identity=%+v; want nonzero handle and object", identity)
	}
	got, err := task20ClassifyHandle(handle, identity)
	if err != nil {
		t.Fatalf("classify handle %#x: %v", uintptr(handle), err)
	}
	owned, err := got.applicationOwned()
	if err != nil {
		t.Fatalf("application-owned policy for classification=%+v: %v", got, err)
	}
	if got.Identity != identity || got.Type != objectType || got.Kind != kind || !owned {
		t.Fatalf("classification=%+v; want identity=%+v type=%q kind=%v application-owned", got, identity, objectType, kind)
	}
}

func TestTask20ObjectTypeQueryGrowsGeometrically(t *testing.T) {
	var sizes []uint32
	typeName, err := task20QueryObjectTypeWith(1, func(_ windows.Handle, buffer []byte, needed *uint32) uint32 {
		sizes = append(sizes, uint32(len(buffer)))
		if len(sizes) < 3 {
			*needed = uint32(len(buffer)) + 32
			return task20StatusInfoLengthMismatch
		}
		writeTask20ObjectTypeFixture(t, buffer, "Process")
		return 0
	})
	if err != nil || typeName != "Process" {
		t.Fatalf("type=%q err=%v", typeName, err)
	}
	if len(sizes) != 3 || sizes[1] < sizes[0]*2 || sizes[2] < sizes[1]*2 {
		t.Fatalf("query sizes=%v", sizes)
	}
}

func TestTask20ObjectTypeQueryRejectsOversizedResponse(t *testing.T) {
	_, err := task20QueryObjectTypeWith(1, func(_ windows.Handle, _ []byte, needed *uint32) uint32 {
		*needed = task20MaxHandleSnapshotSize + 1
		return task20StatusInfoLengthMismatch
	})
	if err == nil {
		t.Fatal("oversized object type response succeeded")
	}
}

func TestTask20ObjectTypeParserRejectsEscapedUnicodeBuffer(t *testing.T) {
	buffer := make([]byte, 64)
	header := (*task20UnicodeString)(unsafe.Pointer(&buffer[0]))
	header.Length, header.MaximumLength = 2, 2
	escaped := new(uint16)
	header.Buffer = escaped
	_, err := task20ParseObjectType(buffer)
	runtime.KeepAlive(escaped)
	if err == nil {
		t.Fatal("out-of-buffer object type string succeeded")
	}
}

func TestTask20ObjectTypeParserRejectsMalformedResponses(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(*testing.T) ([]byte, any)
	}{
		{
			name: "short header",
			prepare: func(_ *testing.T) ([]byte, any) {
				return make([]byte, int(unsafe.Sizeof(task20UnicodeString{}))-1), nil
			},
		},
		{
			name: "misaligned header",
			prepare: func(_ *testing.T) ([]byte, any) {
				buffer := make([]byte, int(unsafe.Sizeof(task20UnicodeString{}))+1)
				return buffer[1:], nil
			},
		},
		{
			name: "length exceeds maximum",
			prepare: func(t *testing.T) ([]byte, any) {
				buffer := make([]byte, 64)
				writeTask20ObjectTypeFixture(t, buffer, "P")
				header := (*task20UnicodeString)(unsafe.Pointer(&buffer[0]))
				header.Length = 4
				header.MaximumLength = 2
				return buffer, nil
			},
		},
		{
			name: "odd Length",
			prepare: func(t *testing.T) ([]byte, any) {
				buffer := make([]byte, 64)
				writeTask20ObjectTypeFixture(t, buffer, "P")
				header := (*task20UnicodeString)(unsafe.Pointer(&buffer[0]))
				header.Length = 1
				return buffer, nil
			},
		},
		{
			name: "odd MaximumLength",
			prepare: func(t *testing.T) ([]byte, any) {
				buffer := make([]byte, 64)
				writeTask20ObjectTypeFixture(t, buffer, "P")
				header := (*task20UnicodeString)(unsafe.Pointer(&buffer[0]))
				header.MaximumLength = 3
				return buffer, nil
			},
		},
		{
			name: "nil pointer",
			prepare: func(t *testing.T) ([]byte, any) {
				buffer := make([]byte, 64)
				writeTask20ObjectTypeFixture(t, buffer, "P")
				header := (*task20UnicodeString)(unsafe.Pointer(&buffer[0]))
				header.Buffer = nil
				return buffer, nil
			},
		},
		{
			name: "misaligned pointer",
			prepare: func(t *testing.T) ([]byte, any) {
				buffer := make([]byte, 64)
				writeTask20ObjectTypeFixture(t, buffer, "P")
				header := (*task20UnicodeString)(unsafe.Pointer(&buffer[0]))
				misaligned := make([]byte, 2)
				header.Buffer = (*uint16)(unsafe.Pointer(&misaligned[1]))
				return buffer, misaligned
			},
		},
		{
			name: "escaped MaximumLength",
			prepare: func(t *testing.T) ([]byte, any) {
				buffer := make([]byte, 64)
				writeTask20ObjectTypeFixture(t, buffer, "P")
				header := (*task20UnicodeString)(unsafe.Pointer(&buffer[0]))
				header.MaximumLength = uint16(len(buffer))
				return buffer, nil
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			buffer, keepAlive := test.prepare(t)
			_, err := task20ParseObjectType(buffer)
			runtime.KeepAlive(keepAlive)
			if err == nil {
				t.Fatal("malformed object type response succeeded")
			}
		})
	}
}

func writeTask20ObjectTypeFixture(t *testing.T, buffer []byte, typeName string) {
	t.Helper()
	encoded, err := windows.UTF16FromString(typeName)
	if err != nil {
		t.Fatal(err)
	}
	headerSize := int(unsafe.Sizeof(task20UnicodeString{}))
	if len(buffer) < headerSize+len(encoded)*2 {
		t.Fatalf("object type fixture buffer=%d; want at least %d", len(buffer), headerSize+len(encoded)*2)
	}
	header := (*task20UnicodeString)(unsafe.Pointer(&buffer[0]))
	header.Length = uint16((len(encoded) - 1) * 2)
	header.MaximumLength = uint16(len(encoded) * 2)
	header.Buffer = (*uint16)(unsafe.Pointer(&buffer[headerSize]))
	for index, value := range encoded {
		*(*uint16)(unsafe.Pointer(&buffer[headerSize+index*2])) = value
	}
}
