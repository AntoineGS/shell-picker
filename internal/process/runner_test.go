package process

import (
	"context"
	"errors"
	"testing"
)

func TestRunnerExecuteReceivesFinalSpecWithoutLaunching(t *testing.T) {
	want := errors.New("synthetic result")
	type contextKey struct{}
	ctx := context.WithValue(context.Background(), contextKey{}, "value")
	called, observed := 0, 0
	runner := Runner{Observe: func(ProcessEvent) { observed++ }, Execute: func(got context.Context, spec Spec) error {
		called++
		if got != ctx || got.Value(contextKey{}) != "value" {
			t.Fatalf("Execute context was replaced: %v", got)
		}
		if spec.Path != "/captured" || len(spec.Args) != 1 || spec.Args[0] != "argument" {
			t.Fatalf("captured spec=%+v", spec)
		}
		return want
	}}
	err := runner.Run(ctx, Spec{Path: "/captured", Args: []string{"argument"}, Containment: ContainmentOwnTree})
	if !errors.Is(err, want) || called != 1 || observed != 0 {
		t.Fatalf("Run error=%v calls=%d observations=%d", err, called, observed)
	}
}

func TestRunnerExecuteDoesNotBypassSpecValidation(t *testing.T) {
	called := false
	runner := Runner{Execute: func(context.Context, Spec) error {
		called = true
		return nil
	}}
	if err := runner.Run(context.Background(), Spec{}); err == nil || called {
		t.Fatalf("invalid spec error=%v Execute called=%v", err, called)
	}
}
