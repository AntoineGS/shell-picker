//go:build windows

package process

import (
	"bufio"
	"context"
	"errors"
	"os"
	"strconv"
	"testing"
	"time"
)

func TestWindowsCancelTerminatesJob(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	child, err := (Runner{}).Start(ctx, helperSpec("block"))
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := child.Wait(); !errors.Is(err, context.Canceled) {
		t.Fatalf("wait=%v", err)
	}
}

func TestWindowsForegroundAndInheritedJobsTerminateDescendants(t *testing.T) {
	for _, containment := range []Containment{ContainmentForegroundTree, ContainmentInheritTree} {
		t.Run(strconv.Itoa(int(containment)), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			read, write, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer read.Close()
			defer write.Close()
			spec := helperSpec("spawn")
			spec.Containment, spec.Stdout = containment, write
			child, err := (Runner{}).Start(ctx, spec)
			if err != nil {
				t.Fatal(err)
			}
			_ = write.Close()
			line, err := bufio.NewReader(read).ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			pid, err := strconv.Atoi(line[:len(line)-1])
			if err != nil {
				t.Fatal(err)
			}
			cancel()
			if err := child.Wait(); !errors.Is(err, context.Canceled) {
				t.Fatalf("wait=%v", err)
			}
			assertProcessGoneWithin(t, pid, 3*time.Second)
		})
	}
}
