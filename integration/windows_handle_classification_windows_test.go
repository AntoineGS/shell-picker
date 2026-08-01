//go:build windows

package integration

import (
	"runtime"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

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
