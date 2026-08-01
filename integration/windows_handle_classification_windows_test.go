//go:build windows

package integration

import (
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
	header.Buffer = (*uint16)(unsafe.Pointer(uintptr(unsafe.Pointer(&buffer[0])) + uintptr(len(buffer))))
	if _, err := task20ParseObjectType(buffer); err == nil {
		t.Fatal("out-of-buffer object type string succeeded")
	}
}

func writeTask20ObjectTypeFixture(t *testing.T, buffer []byte, typeName string) {
	t.Helper()
	encoded, err := windows.UTF16FromString(typeName)
	if err != nil {
		t.Fatal(err)
	}
	headerSize := unsafe.Sizeof(task20UnicodeString{})
	if len(buffer) < int(headerSize)+len(encoded)*2 {
		t.Fatalf("object type fixture buffer=%d; want at least %d", len(buffer), int(headerSize)+len(encoded)*2)
	}
	header := (*task20UnicodeString)(unsafe.Pointer(&buffer[0]))
	header.Length = uint16((len(encoded) - 1) * 2)
	header.MaximumLength = uint16(len(encoded) * 2)
	header.Buffer = (*uint16)(unsafe.Pointer(uintptr(unsafe.Pointer(&buffer[0])) + headerSize))
	for index, value := range encoded {
		*(*uint16)(unsafe.Pointer(uintptr(unsafe.Pointer(&buffer[0])) + headerSize + uintptr(index)*2)) = value
	}
}
