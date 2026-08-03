package integration

import (
	"errors"
	"fmt"
	"testing"
)

func TestDedicatedTraceZoxideOutcomeUsesMeasuredGeneration(t *testing.T) {
	events := []traceEvent{
		{Event: "generation.publish", Generation: 1, ZoxideOutcome: "missing"},
		{Event: "generation.publish", Generation: 2, ZoxideOutcome: "process-error"},
	}
	got, err := traceZoxideOutcome(events, 2)
	if err != nil || got != "process-error" {
		t.Fatalf("outcome=%q err=%v", got, err)
	}
	if _, err := traceZoxideOutcome(events, 3); err == nil {
		t.Fatal("missing measured generation accepted")
	}
}

func assertDedicatedZoxideOutcome(events []traceEvent, scenario dedicatedScenario) error {
	expected, ok := expectedZoxideOutcome(scenario.zoxideMode)
	if !ok {
		return nil
	}
	outcome := traceBenchmarkZoxideOutcome(events, scenario.generation)
	if outcome == "" {
		return errors.New("missing measured zoxide outcome")
	}
	if outcome != expected {
		return fmt.Errorf("zoxide mode %q produced outcome %q; want %q", scenario.zoxideMode, outcome, expected)
	}
	return nil
}

func traceZoxideOutcome(events []traceEvent, generation uint64) (string, error) {
	found := false
	outcome := ""
	for _, event := range events {
		if event.Event != "generation.publish" || generation != 0 && event.Generation != generation {
			continue
		}
		if found && event.ZoxideOutcome != outcome {
			return "", fmt.Errorf("measured generation has conflicting zoxide outcomes %q and %q", outcome, event.ZoxideOutcome)
		}
		found = true
		outcome = event.ZoxideOutcome
	}
	if !found {
		return "", errors.New("missing measured zoxide outcome")
	}
	return outcome, nil
}
