//go:build windows

package integration

import (
	"errors"
	"fmt"
	"net"
	"os"
	"runtime"
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

func task20ClassifyHandle(handle windows.Handle, identity task20HandleIdentity) (task20ResourceIdentity, error) {
	typeName, err := task20QueryObjectType(handle)
	if err != nil {
		return task20ResourceIdentity{}, fmt.Errorf("query handle %#x object type: %w", uintptr(handle), err)
	}
	if typeName == "File" {
		if _, err := windows.Getsockname(handle); err == nil {
			return task20ResourceIdentity{Identity: identity, Type: typeName, Kind: task20HandleSocket}, nil
		} else if !errors.Is(err, windows.WSAENOTSOCK) {
			return task20ResourceIdentity{}, fmt.Errorf("probe handle %#x as socket: %w", uintptr(handle), err)
		}

		fileType, err := windows.GetFileType(handle)
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
		if (task20ResourceIdentity{Kind: kind}).applicationOwned() {
			t.Errorf("runtime kind=%v was classified as application-owned", kind)
		}
	}
}

func TestTask20NativeHandleClassification(t *testing.T) {
	t.Run("File", func(t *testing.T) {
		file, err := os.CreateTemp(t.TempDir(), "task20-file-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = file.Close() })
		assertTask20NativeHandleClassification(t, windows.Handle(file.Fd()), "File", task20HandleFile)
	})

	t.Run("Pipe", func(t *testing.T) {
		reader, writer, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = reader.Close() })
		t.Cleanup(func() { _ = writer.Close() })
		assertTask20NativeHandleClassification(t, windows.Handle(reader.Fd()), "File", task20HandlePipe)
		assertTask20NativeHandleClassification(t, windows.Handle(writer.Fd()), "File", task20HandlePipe)
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
		assertTask20NativeHandleClassification(t, windows.Handle(file.Fd()), "File", task20HandleSocket)
	})

	t.Run("Process", func(t *testing.T) {
		assertTask20NativeHandleClassification(t, windows.CurrentProcess(), "Process", task20HandleProcess)
	})

	t.Run("Job", func(t *testing.T) {
		job, err := windows.CreateJobObject(nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = windows.CloseHandle(job) })
		assertTask20NativeHandleClassification(t, job, "Job", task20HandleJob)
	})
}

func assertTask20NativeHandleClassification(t *testing.T, handle windows.Handle, objectType string, kind task20HandleKind) {
	t.Helper()
	identity := task20HandleIdentity{Value: uintptr(handle)}
	got, err := task20ClassifyHandle(handle, identity)
	if err != nil {
		t.Fatalf("classify handle %#x: %v", uintptr(handle), err)
	}
	if got.Identity != identity || got.Type != objectType || got.Kind != kind || !got.applicationOwned() {
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
