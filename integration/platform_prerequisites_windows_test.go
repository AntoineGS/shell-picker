//go:build windows

package integration

import (
	"testing"

	"golang.org/x/sys/windows"
)

func TestPlatformPrerequisites(t *testing.T) {
	if windows.RtlGetVersion().BuildNumber < 17763 {
		t.Fatalf("Windows build %d is below ConPTY minimum 17763", windows.RtlGetVersion().BuildNumber)
	}
	var inputRead, inputWrite, outputRead, outputWrite windows.Handle
	if err := windows.CreatePipe(&inputRead, &inputWrite, nil, 0); err != nil {
		t.Fatalf("CreatePipe input: %v", err)
	}
	defer windows.CloseHandle(inputRead)
	defer windows.CloseHandle(inputWrite)
	if err := windows.CreatePipe(&outputRead, &outputWrite, nil, 0); err != nil {
		t.Fatalf("CreatePipe output: %v", err)
	}
	defer windows.CloseHandle(outputRead)
	defer windows.CloseHandle(outputWrite)
	var console windows.Handle
	if err := windows.CreatePseudoConsole(windows.Coord{X: 80, Y: 25}, inputRead, outputWrite, 0, &console); err != nil {
		t.Fatalf("CreatePseudoConsole: %v", err)
	}
	windows.ClosePseudoConsole(console)
}
