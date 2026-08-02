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
	"os/signal"
	"reflect"
	"runtime"
	"strconv"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

func platformHelper(args []string) bool {
	switch args[0] {
	case "foreground-probe":
		probe := foregroundProbe{}
		fd, _ := strconv.Atoi(args[1])
		tty := os.NewFile(uintptr(fd), "child-tty")
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
	case "retain-tree-session":
		spec := helperSpec("exit", "0")
		spec.Containment = ContainmentInheritTree
		child, err := (Runner{}).Start(context.Background(), spec)
		if err != nil {
			return true
		}
		tree, err := child.RetainTree()
		if err != nil || child.Wait() != nil || tree.KillTree() != nil {
			return true
		}
		os.Exit(3)
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
func TestRetainedInheritedTreeKillsGroupAfterChildWait(t *testing.T) {
	cmd := helperExec("retain-tree-session")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Run(); err == nil {
		t.Fatal("post-Wait retained tree kill returned to callback")
	}
}
func TestProcessGroupLeaderRemainsUnreapedThroughCleanup(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux zombie-state lifecycle assertion")
	}
	stream := newBlockingStream()
	spec := helperSpec("print-args", "x")
	spec.Stdout, spec.WaitDelay = stream, 50*time.Millisecond
	child, err := (Runner{}).Start(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	<-stream.blocked
	<-child.observedExit
	if !processZombie(child.PID()) {
		t.Fatalf("pid %d was reaped before pump cleanup", child.PID())
	}
	if err := child.KillTree(); err != nil {
		t.Fatalf("KillTree in exit-cleanup window: %v", err)
	}
	if err := child.Wait(); !errors.Is(err, ErrWaitDelay) {
		t.Fatalf("Wait=%v", err)
	}
	assertProcessGoneWithin(t, child.PID(), time.Second)
}
func TestForegroundRejectedBeforeLaunchWhenMaskUnsupported(t *testing.T) {
	original := foregroundRestoreSupported
	foregroundRestoreSupported = func() bool { return false }
	defer func() { foregroundRestoreSupported = original }()
	file, err := os.Open("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var events []ProcessEvent
	_, err = (Runner{Observe: func(event ProcessEvent) { events = append(events, event) }}).Start(context.Background(),
		Spec{Path: os.Args[0], Containment: ContainmentForegroundTree, ForegroundTTY: file})
	if err == nil || len(events) != 0 {
		t.Fatalf("err=%v events=%+v", err, events)
	}
}
func TestSIGTTOUMaskSetsNumericSignalBit(t *testing.T) {
	mask := sigttouMask()
	root := reflect.ValueOf(mask)
	if root.Kind() != reflect.Struct {
		t.Skip("platform sigset has no word struct")
	}
	value := root.FieldByName("Val")
	if !value.IsValid() {
		t.Skip("platform sigset has no Val words")
	}
	index := int(syscall.SIGTTOU) - 1
	wordBits := int(value.Index(0).Type().Bits())
	if value.Index(index/wordBits).Uint()&(uint64(1)<<uint(index%wordBits)) == 0 {
		t.Fatalf("SIGTTOU bit absent from %+v", mask)
	}
}

type foregroundTTYReport struct {
	ParentTTYFD                  int    `json:"parent_tty_fd"`
	ChildTTYFD                   int    `json:"child_tty_fd"`
	SameTTY                      bool   `json:"same_tty"`
	Input                        string `json:"input"`
	RestoredPreviousGroup        bool   `json:"restored_previous_group"`
	RestoredThreadMask           bool   `json:"restored_thread_mask"`
	PreservedSIGTTOUNotification bool   `json:"preserved_sigttou_notification"`
	DescriptorDelta              int    `json:"descriptor_delta"`
	Err                          string `json:"err,omitempty"`
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
	if report.Err != "" || report.ParentTTYFD <= 3 ||
		report.ChildTTYFD != 4 || !report.SameTTY || report.Input != "x\n" ||
		!report.RestoredPreviousGroup || !report.RestoredThreadMask || !report.PreservedSIGTTOUNotification || report.DescriptorDelta != 0 {
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
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	beforeMask, err := currentThreadMask()
	if err != nil {
		report.Err = err.Error()
		return
	}
	notifications := make(chan os.Signal, 1)
	signal.Notify(notifications, syscall.SIGTTOU)
	defer signal.Stop(notifications)
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
	extra, err := os.Open("/dev/null")
	if err != nil {
		report.Err = err.Error()
		return
	}
	defer extra.Close()
	spec := helperSpec("foreground-probe", "4")
	spec.Containment, spec.ForegroundTTY = ContainmentForegroundTree, tty
	spec.ExtraFiles = []*os.File{extra}
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
	if _, err := extra.Stat(); err != nil {
		report.Err = "caller extra file closed: " + err.Error()
		return
	}
	report.SameTTY, report.Input = probe.SameTTY, probe.Input
	if probe.Err != "" {
		report.Err = probe.Err
		return
	}
	current, err := foregroundPGR(int(tty.Fd()))
	report.RestoredPreviousGroup = err == nil && current == previous
	afterMask, maskErr := currentThreadMask()
	report.RestoredThreadMask = maskErr == nil && reflect.DeepEqual(beforeMask, afterMask)
	_ = syscall.Kill(os.Getpid(), syscall.SIGTTOU)
	select {
	case <-notifications:
		report.PreservedSIGTTOUNotification = true
	case <-time.After(time.Second):
	}
	return
}
func TestRestoreForegroundPGRRestoresMaskOnIoctlError(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	before, err := currentThreadMask()
	if err != nil {
		t.Fatal(err)
	}
	if err := restoreForegroundPGR(-1, 1); err == nil {
		t.Fatal("invalid tty restore succeeded")
	}
	after, err := currentThreadMask()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("mask changed: before=%+v after=%+v", before, after)
	}
}
func TestRestoreForegroundPGRRestoresMaskWhenRestoreReportsError(t *testing.T) {
	realMask, realIoctl := pthreadSigmask, setForegroundPGR
	defer func() { pthreadSigmask, setForegroundPGR = realMask, realIoctl }()
	want := errors.New("restore mask")
	calls := 0
	pthreadSigmask = func(how int, set, old *threadSigset) error {
		calls++
		err := realMask(how, set, old)
		if calls == 2 {
			return want
		}
		return err
	}
	setForegroundPGR = func(int, *int) error { return nil }
	if err := restoreForegroundPGR(0, 1); !errors.Is(err, want) {
		t.Fatalf("restore=%v", err)
	}
	if calls != 2 {
		t.Fatalf("mask calls=%d", calls)
	}
}
func currentThreadMask() (threadSigset, error) {
	var mask threadSigset
	err := pthreadSigmask(threadSigSetmask, nil, &mask)
	return mask, err
}
func TestUnixSharedStdoutStderrWriterIsSerialized(t *testing.T) {
	writer := &unixConcurrencyWriter{}
	spec := helperSpec("both-streams")
	spec.Stdout, spec.Stderr = writer, writer
	if err := (Runner{}).Run(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if writer.concurrent.Load() != 0 {
		t.Fatalf("concurrent writes=%d", writer.concurrent.Load())
	}
}

type unixConcurrencyWriter struct{ active, concurrent atomic.Int32 }

func (w *unixConcurrencyWriter) Write(p []byte) (int, error) {
	if w.active.Add(1) != 1 {
		w.concurrent.Add(1)
	}
	for i := 0; i < 10000; i++ {
	}
	w.active.Add(-1)
	return len(p), nil
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
func platformResourceCount(t *testing.T) uint64 { return uint64(openDescriptorCount(t)) }
func assertPlatformResourcesReturn(t *testing.T, want uint64) {
	assertDescriptorCountReturns(t, int(want))
}
