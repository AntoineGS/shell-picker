package process

import (
	"context"
	"errors"
	"testing"
)

func TestRunnerBeforeStartInspectsValidatedSpecAndCannotSynthesizeSuccess(t *testing.T) {
	want := errors.New("stop before start")
	ctx := context.Background()
	called, observed := 0, 0
	runner := Runner{Observe: func(ProcessEvent) { observed++ }, BeforeStart: func(spec Spec) error {
		called++
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

func TestRunnerBeforeStartRunsOnlyAfterSpecValidation(t *testing.T) {
	called := false
	runner := Runner{BeforeStart: func(Spec) error {
		called = true
		return nil
	}}
	if err := runner.Run(context.Background(), Spec{}); err == nil || called {
		t.Fatalf("invalid spec error=%v BeforeStart called=%v", err, called)
	}
}

func TestRunnerNilBeforeStartExecutesRealStart(t *testing.T) {
	if err := (Runner{}).Run(context.Background(), Spec{Path: "go", Args: []string{"version"},
		Containment: ContainmentOwnTree}); err != nil {
		t.Fatalf("nil BeforeStart did not execute real process: %v", err)
	}
}

func TestRunnerBeforeStartReturningNilCannotAvoidRealStart(t *testing.T) {
	inspections, starts := 0, 0
	runner := Runner{BeforeStart: func(Spec) error {
		inspections++
		return nil
	}, Observe: func(event ProcessEvent) {
		if event.Phase == "start" {
			starts++
		}
	}}
	if err := runner.Run(context.Background(), Spec{Path: "go", Args: []string{"version"},
		Containment: ContainmentOwnTree}); err != nil {
		t.Fatal(err)
	}
	if inspections != 1 || starts != 1 {
		t.Fatalf("inspections=%d starts=%d; want one real start after inspection", inspections, starts)
	}
}
