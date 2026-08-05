//go:build windows

package integration

import (
	"errors"
	"reflect"
	"testing"
)

func TestWindowsVerifiedProcessExitsRejectsLiveRecordedIdentity(t *testing.T) {
	records := []descendantProcessRecord{{PID: 42, Identity: "42:100", CommandLine: "fzf"}}
	nodes := map[uint32]windowsProcessNode{42: {pid: 42, creationMarker: 100}}
	if _, err := verifiedWindowsProcessExits(records, nodes); err == nil {
		t.Fatal("live recorded process identity was accepted as exited")
	}
}

func TestWindowsVerifiedProcessExitsAcceptsAbsentAndReusedPIDs(t *testing.T) {
	records := []descendantProcessRecord{
		{PID: 42, Identity: "42:100", CommandLine: "fzf"},
		{PID: 43, Identity: "43:200", CommandLine: "eza"},
	}
	nodes := map[uint32]windowsProcessNode{43: {pid: 43, creationMarker: 201}}
	got, err := verifiedWindowsProcessExits(records, nodes)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"42:100", "43:200"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("verified exits=%v want %v", got, want)
	}
}

func TestWindowsVerifiedProcessExitsRejectsUnverifiableReusedPID(t *testing.T) {
	records := []descendantProcessRecord{{PID: 42, Identity: "42:100", CommandLine: "fzf"}}
	nodes := map[uint32]windowsProcessNode{42: {pid: 42, queryErr: errors.New("access denied")}}
	if _, err := verifiedWindowsProcessExits(records, nodes); err == nil {
		t.Fatal("unverifiable recorded process identity was accepted as exited")
	}
}
