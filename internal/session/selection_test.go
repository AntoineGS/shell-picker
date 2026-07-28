package session

import (
	"bytes"
	"errors"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/pathutil"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestValidateCDRequiresOneExactFilesystemDirectoryRecord(t *testing.T) {
	directory := eventRecord(protocol.KindDirectory, "directory", "/work/directory")
	local := eventRecord(protocol.KindLocal, "local", "/work/local")
	virtual := eventRecord(protocol.KindVirtual, "Drives", "ignored")
	s := eventSnapshot(protocol.PickerCD, protocol.ModeNormal, pathutil.Filesystem([]byte("/work")), directory, local, virtual)

	for _, record := range []string{directory.FullKey(), local.FullKey()} {
		outcome, err := ValidateCD(s, [][]byte{[]byte(record)})
		if err != nil || outcome.Status != protocol.StatusAccepted || len(outcome.Paths) != 1 || !bytes.HasPrefix(outcome.Paths[0], []byte("/work/")) {
			t.Fatalf("record=%q outcome=%+v err=%v", record, outcome, err)
		}
	}
	for _, test := range []struct {
		name     string
		accepted [][]byte
		want     error
	}{
		{"none", nil, ErrInvalidSelection},
		{"many", [][]byte{[]byte(directory.FullKey()), []byte(local.FullKey())}, ErrInvalidSelection},
		{"malformed", [][]byte{[]byte("directory\tname\t!!!")}, ErrInvalidSelection},
		{"unknown display", [][]byte{eventRecord(protocol.KindDirectory, "changed", "/work/directory").Wire().Bytes()}, ErrUnknownSelection},
		{"virtual", [][]byte{[]byte(virtual.FullKey())}, ErrInvalidSelection},
	} {
		_, err := ValidateCD(s, test.accepted)
		if !errors.Is(err, test.want) {
			t.Fatalf("%s error=%v want=%v", test.name, err, test.want)
		}
	}
}

func TestValidateCPRestoresVisibleOrderAndDuplicateMultiplicity(t *testing.T) {
	one := eventRecord(protocol.KindFile, "one", "/work/one")
	two := eventRecord(protocol.KindDirectory, "two", "/work/two")
	s := eventSnapshot(protocol.PickerCP, protocol.ModeNormal, pathutil.Filesystem([]byte("/elsewhere")), one, two, one)
	accepted := [][]byte{[]byte(two.FullKey()), []byte(one.FullKey()), []byte(one.FullKey())}
	outcome, err := ValidateCP(s, accepted, []byte("/work"))
	if err != nil || outcome.Status != protocol.StatusAccepted {
		t.Fatalf("outcome=%+v err=%v", outcome, err)
	}
	want := [][]byte{[]byte("one"), []byte("two"), []byte("one")}
	if len(outcome.Paths) != len(want) {
		t.Fatalf("paths=%q", outcome.Paths)
	}
	for i := range want {
		if !bytes.Equal(outcome.Paths[i], want[i]) {
			t.Fatalf("paths[%d]=%q want=%q", i, outcome.Paths[i], want[i])
		}
	}
}

func TestValidateCPRejectsMalformedUnknownResidualAndVirtual(t *testing.T) {
	one := eventRecord(protocol.KindFile, "one", "/work/one")
	virtual := eventRecord(protocol.KindVirtual, "Drives", "ignored")
	s := eventSnapshot(protocol.PickerCP, protocol.ModeNormal, pathutil.Filesystem([]byte("/work")), one, virtual)
	tests := []struct {
		name     string
		accepted [][]byte
		want     error
	}{
		{"empty", nil, ErrInvalidSelection},
		{"malformed tabs", [][]byte{[]byte("file\tone")}, ErrInvalidSelection},
		{"noncanonical payload", [][]byte{[]byte("file\tone\tL3dvcms")}, ErrInvalidSelection},
		{"unknown kind identity", [][]byte{eventRecord(protocol.KindDirectory, "one", "/work/one").Wire().Bytes()}, ErrUnknownSelection},
		{"unknown display identity", [][]byte{eventRecord(protocol.KindFile, "changed", "/work/one").Wire().Bytes()}, ErrUnknownSelection},
		{"residual duplicate", [][]byte{[]byte(one.FullKey()), []byte(one.FullKey())}, ErrUnknownSelection},
		{"virtual mark", [][]byte{[]byte(one.FullKey()), []byte(virtual.FullKey())}, ErrInvalidSelection},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ValidateCP(s, test.accepted, []byte("/work"))
			if !errors.Is(err, test.want) {
				t.Fatalf("error=%v want=%v", err, test.want)
			}
		})
	}
}

func TestValidateCPRejectsRecordWithoutAuthoritativeFilesystemTarget(t *testing.T) {
	record := eventRecord(protocol.KindFile, "one", "/work/one")
	record.Target = pathutil.Drives()
	s := eventSnapshot(protocol.PickerCP, protocol.ModeNormal, pathutil.Filesystem([]byte("/work")), record)
	if _, err := ValidateCP(s, [][]byte{[]byte(record.FullKey())}, []byte("/work")); !errors.Is(err, ErrInvalidSelection) {
		t.Fatalf("error=%v", err)
	}
}
