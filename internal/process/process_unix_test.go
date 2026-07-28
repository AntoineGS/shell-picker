//go:build !windows

package process

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

func platformHelper(args []string) bool {
	switch args[0] {
	case "foreground-probe":
		probe := foregroundProbe{}
		tty := os.NewFile(3, "child-tty")
		other, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
		if err == nil {
			var a, b syscall.Stat_t
			err = syscall.Fstat(int(tty.Fd()), &a)
			if err == nil {
				err = syscall.Fstat(int(other.Fd()), &b)
			}
			probe.SameTTY = err == nil && a.Dev == b.Dev && a.Rdev == b.Rdev
			_ = other.Close()
		}
		if err == nil {
			data := make([]byte, 2)
			_, err = io.ReadFull(tty, data)
			probe.Input = string(data)
		}
		if err != nil {
			probe.Err = err.Error()
		}
		_ = json.NewEncoder(os.Stdout).Encode(probe)
		_ = tty.Close()
		return true
	case "inherit-session":
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()
		spec := helperSpec("pid-block")
		spec.Containment, spec.Stdout = ContainmentInheritTree, os.Stdout
		_ = (Runner{}).Run(ctx, spec)
		return true
	}
	return false
}

func TestCancelKillsOwnedProcessTreeEventually(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	spec := helperSpec("spawn")
	read, write := testPipe(t)
	spec.Stdout = write
	child, err := (Runner{}).Start(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	_ = write.Close()
	pid := readPID(t, read)
	cancel()
	if err := child.Wait(); !errors.Is(err, context.Canceled) {
		t.Fatalf("wait=%v", err)
	}
	assertProcessGoneWithin(t, pid, 3*time.Second)
}

func TestInheritedTreeCancellationKillsCallbackGroup(t *testing.T) {
	cmd := helperExec("inherit-session")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	read, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := readPID(t, read)
	if err := cmd.Wait(); err == nil {
		t.Fatal("inherited callback group survived cancellation")
	}
	assertProcessGoneWithin(t, pid, 3*time.Second)
}

type foregroundTTYReport struct {
	ParentTTYFD           int    `json:"parent_tty_fd"`
	ChildTTYFD            int    `json:"child_tty_fd"`
	SameTTY               bool   `json:"same_tty"`
	Input                 string `json:"input"`
	RestoredPreviousGroup bool   `json:"restored_previous_group"`
	DescriptorDelta       int    `json:"descriptor_delta"`
	Err                   string `json:"err,omitempty"`
}

type foregroundProbe struct {
	SameTTY bool   `json:"same_tty"`
	Input   string `json:"input"`
	Err     string `json:"err,omitempty"`
}

func TestForegroundTreeOwnsTTYAndRestoresPreviousGroup(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux PTY ioctl test")
	}
	baseline := openDescriptorCount(t)
	terminal := startTestPTY(t)
	reportR, reportW := testPipe(t)
	slave := terminal.slave
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	helper := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestForegroundTTYSessionHelper$")
	helper.Env = append(os.Environ(), "GO_WANT_FOREGROUND_TTY_SESSION=1")
	helper.Stdin, helper.Stdout, helper.Stderr = slave, slave, slave
	helper.ExtraFiles = []*os.File{reportW}
	helper.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	waited := false
	t.Cleanup(func() {
		cancel()
		if !waited && helper.Process != nil {
			_ = helper.Process.Kill()
			_ = helper.Wait()
		}
		_ = reportR.Close()
		_ = reportW.Close()
		terminal.close()
	})
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	_ = reportW.Close()
	_ = slave.Close()
	if _, err := terminal.master.Write([]byte("x\n")); err != nil {
		t.Fatal(err)
	}
	var report foregroundTTYReport
	if err := json.NewDecoder(reportR).Decode(&report); err != nil {
		t.Fatal(err)
	}
	waitErr := helper.Wait()
	waited = true
	cancel()
	if waitErr != nil {
		t.Fatal(waitErr)
	}
	_ = reportR.Close()
	terminal.close()
	if report.Err != "" || report.ParentTTYFD <= 3 || report.ParentTTYFD == report.ChildTTYFD ||
		report.ChildTTYFD != 3 || !report.SameTTY || report.Input != "x\n" ||
		!report.RestoredPreviousGroup || report.DescriptorDelta != 0 {
		t.Fatalf("report=%+v", report)
	}
	assertDescriptorCountReturns(t, baseline)
}

func TestForegroundTTYSessionHelper(t *testing.T) {
	if os.Getenv("GO_WANT_FOREGROUND_TTY_SESSION") != "1" {
		return
	}
	reportFile := os.NewFile(3, "foreground-report")
	report := runForegroundTTYSession()
	if err := json.NewEncoder(reportFile).Encode(report); err != nil {
		t.Fatal(err)
	}
	if err := reportFile.Close(); err != nil {
		t.Fatal(err)
	}
}

func runForegroundTTYSession() (report foregroundTTYReport) {
	warmR, warmW, err := os.Pipe()
	if err != nil {
		report.Err = err.Error()
		return
	}
	_ = warmR.Close()
	_ = warmW.Close()
	baseline := descriptorCount()
	previous, err := foregroundPGR(0)
	if err != nil {
		report.Err = err.Error()
		return
	}
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		report.Err = err.Error()
		return
	}
	report.ParentTTYFD = int(tty.Fd())
	inR, inW, _ := os.Pipe()
	outR, outW, _ := os.Pipe()
	errR, errW, _ := os.Pipe()
	files := []*os.File{tty, inR, inW, outR, outW, errR, errW}
	defer func() {
		for _, file := range files {
			_ = file.Close()
		}
		report.DescriptorDelta = descriptorCount() - baseline
	}()
	spec := helperSpec("foreground-probe")
	spec.Containment, spec.ForegroundTTY = ContainmentForegroundTree, tty
	spec.Stdin, spec.Stdout, spec.Stderr = inR, outW, errW
	child, err := (Runner{}).Start(context.Background(), spec)
	if err != nil {
		report.Err = err.Error()
		return
	}
	report.ChildTTYFD = child.childTTYFD
	_ = inR.Close()
	_ = outW.Close()
	_ = errW.Close()
	var probe foregroundProbe
	if err := json.NewDecoder(outR).Decode(&probe); err != nil {
		report.Err = err.Error()
		_ = child.KillTree()
		_ = child.Wait()
		return
	}
	if err := child.Wait(); err != nil {
		report.Err = err.Error()
		return
	}
	report.SameTTY, report.Input = probe.SameTTY, probe.Input
	if probe.Err != "" {
		report.Err = probe.Err
		return
	}
	current, err := foregroundPGR(int(tty.Fd()))
	report.RestoredPreviousGroup = err == nil && current == previous
	return
}

type testPTY struct{ master, slave *os.File }

func startTestPTY(t *testing.T) *testPTY {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	unlock := int32(0)
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), 0x40045431, uintptr(unsafe.Pointer(&unlock))); errno != 0 {
		t.Fatal(errno)
	}
	var number uint32
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), 0x80045430, uintptr(unsafe.Pointer(&number))); errno != 0 {
		t.Fatal(errno)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	return &testPTY{master: master, slave: slave}
}

func (p *testPTY) close() { _ = p.master.Close(); _ = p.slave.Close() }

func testPipe(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close(); _ = w.Close() })
	return r, w
}

func readPID(t *testing.T, reader io.Reader) int {
	t.Helper()
	line, err := bufio.NewReader(reader).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(line[:len(line)-1])
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

func assertProcessGoneWithin(t *testing.T, pid int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) || processZombie(pid) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("process %d still exists", pid)
}

func processZombie(pid int) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	for i := 0; i+3 < len(data); i++ {
		if string(data[i:i+3]) == ") Z" {
			return true
		}
	}
	return false
}

func openDescriptorCount(t *testing.T) int { t.Helper(); return descriptorCount() }
func descriptorCount() int                 { entries, _ := os.ReadDir("/proc/self/fd"); return len(entries) }
func assertDescriptorCountReturns(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if descriptorCount() <= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("descriptor count=%d want<=%d", descriptorCount(), want)
}
