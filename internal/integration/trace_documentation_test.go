package integration

import (
	"encoding/csv"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestDocumentedTraceTableMatchesValidator(t *testing.T) {
	want := traceContractRows(t)
	got := documentedTraceContractRows(t)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("documented trace contract differs from validator\n got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func traceContractRows(t *testing.T) []string {
	candidates := traceStringCandidates(t)
	renderers := []string{}
	for _, candidate := range rendererStringCandidates(candidates) {
		if validRenderer(candidate) {
			renderers = append(renderers, candidate)
		}
	}
	renderers = uniqueSorted(renderers)

	outcomes := map[string][]string{}
	for _, event := range candidates {
		for _, outcome := range candidates {
			if validTraceOutcome(event, outcome) {
				outcomes[event] = append(outcomes[event], outcome)
			}
		}
	}

	rows := []string{
		"event,outcomes,generation,renderer,counters,optional_fields",
		"@renderers," + strings.Join(renderers, "|") + ",,,,",
	}
	for event, acceptedOutcomes := range outcomes {
		acceptedOutcomes = uniqueSorted(acceptedOutcomes)
		profiles := map[string][]string{}
		for _, outcome := range acceptedOutcomes {
			base := TraceEvent{Name: event, Outcome: outcome}
			acceptedRenderers := []string{}
			if validateTraceEvent(base) == nil {
				acceptedRenderers = append(acceptedRenderers, "")
			}
			for _, renderer := range renderers {
				candidate := base
				candidate.Renderer = renderer
				if validateTraceEvent(candidate) == nil {
					acceptedRenderers = append(acceptedRenderers, renderer)
				}
			}
			if len(acceptedRenderers) == 0 {
				t.Fatalf("event %q outcome %q has no validator-accepted renderer state", event, outcome)
			}
			base.Renderer = acceptedRenderers[0]
			profile := strings.Join([]string{
				acceptedGenerationStates(t, base),
				documentedRendererState(acceptedRenderers, renderers),
				acceptedCounterStates(t, base, acceptedRenderers),
				strings.Join(acceptedOptionalFields(base), "|"),
			}, ",")
			profiles[profile] = append(profiles[profile], outcome)
		}
		for profile, profileOutcomes := range profiles {
			rows = append(rows, event+","+strings.Join(uniqueSorted(profileOutcomes), "|")+","+profile)
		}
	}
	sort.Strings(rows[1:])
	return rows
}

func rendererStringCandidates(values []string) []string {
	candidates := append([]string(nil), values...)
	for _, base := range values {
		for _, suffix := range values {
			if strings.HasPrefix(suffix, "-") && !strings.ContainsAny(suffix, " \t\r\n") {
				candidates = append(candidates, base+suffix)
			}
		}
	}
	return uniqueSorted(candidates)
}

func traceStringCandidates(t *testing.T) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "trace.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	candidates := []string{""}
	ast.Inspect(file, func(node ast.Node) bool {
		literal, ok := node.(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			return true
		}
		value, err := strconv.Unquote(literal.Value)
		if err == nil {
			candidates = append(candidates, value, value+"-fallback")
		}
		return true
	})
	return uniqueSorted(candidates)
}

func acceptedGenerationStates(t *testing.T, base TraceEvent) string {
	t.Helper()
	states := []string{}
	for _, generation := range []uint64{0, 1} {
		candidate := base
		candidate.Generation = generation
		if validateTraceEvent(candidate) == nil {
			if generation == 0 {
				states = append(states, "0")
			} else {
				states = append(states, "nonzero")
			}
		}
	}
	if len(states) == 0 {
		t.Fatalf("event %q has no accepted generation state", base.Name)
	}
	return strings.Join(states, "|")
}

func documentedRendererState(renderers, allRenderers []string) string {
	empty := renderers[0] == ""
	nonempty := renderers
	if empty {
		nonempty = renderers[1:]
	}
	if len(nonempty) == 0 {
		return "none"
	}
	prefix := "required:"
	if empty {
		prefix = "optional:"
	}
	value := strings.Join(nonempty, "|")
	if strings.Join(nonempty, "|") == strings.Join(allRenderers, "|") {
		value = "@renderers"
	}
	return prefix + value
}

func acceptedCounterStates(t *testing.T, base TraceEvent, renderers []string) string {
	t.Helper()
	groups := map[string][]string{}
	for _, renderer := range renderers {
		states := []string{}
		for childStarts := -1; childStarts <= 4; childStarts++ {
			for maxLive := -1; maxLive <= 2; maxLive++ {
				candidate := base
				candidate.Renderer = renderer
				candidate.ChildStarts = childStarts
				candidate.MaxLiveChildren = maxLive
				if validateTraceEvent(candidate) == nil {
					states = append(states, strconv.Itoa(childStarts)+"/"+strconv.Itoa(maxLive))
				}
			}
		}
		if len(states) == 0 {
			t.Fatalf("event %q renderer %q has no accepted counter state", base.Name, renderer)
		}
		name := renderer
		if name == "" {
			name = "none"
		}
		groups[strings.Join(states, "|")] = append(groups[strings.Join(states, "|")], name)
	}
	if len(groups) == 1 {
		for states := range groups {
			return states
		}
	}
	parts := []string{}
	for states, names := range groups {
		label := strings.Join(uniqueSorted(names), "|")
		if len(names) == len(renderers)-1 && !contains(names, "native") {
			label = "non-native"
		}
		parts = append(parts, label+"="+states)
	}
	sort.Strings(parts)
	return strings.Join(parts, ";")
}

func acceptedOptionalFields(base TraceEvent) []string {
	fields := map[string]func(*TraceEvent){
		"actor_queue_wait_us": func(event *TraceEvent) { event.ActorQueueWait = time.Microsecond },
		"callback_ipc_us":     func(event *TraceEvent) { event.CallbackIPC = time.Microsecond },
		"candidate_count":     func(event *TraceEvent) { event.CandidateCount = 1 },
		"load_us":             func(event *TraceEvent) { event.LoadDuration = time.Microsecond },
		"local_us":            func(event *TraceEvent) { event.LocalDuration = time.Microsecond },
		"path":                func(event *TraceEvent) { event.Path = []byte("path") },
		"transform_us":        func(event *TraceEvent) { event.TransformDuration = time.Microsecond },
	}
	accepted := []string{}
	for name, apply := range fields {
		candidate := base
		apply(&candidate)
		if validateTraceEvent(candidate) == nil {
			accepted = append(accepted, name)
		}
	}
	if states := acceptedZoxideRequiredFields(base); len(states) != 0 {
		accepted = append(accepted, "zoxide_*:"+strings.Join(states, "|"))
	}
	sort.Strings(accepted)
	return accepted
}

func acceptedZoxideRequiredFields(base TraceEvent) []string {
	states := []string{}
	for mask := 1; mask < 4; mask++ {
		candidate := base
		parts := []string{}
		if mask&1 != 0 {
			candidate.ZoxidePolicy = "fresh"
			parts = append(parts, "policy")
		}
		if mask&2 != 0 {
			candidate.ZoxideOutcome = "ok"
			parts = append(parts, "outcome")
		}
		if validateTraceEvent(candidate) == nil {
			states = append(states, strings.Join(parts, "+"))
		}
	}
	sort.Strings(states)
	return states
}

func documentedTraceContractRows(t *testing.T) []string {
	t.Helper()
	document, err := os.ReadFile("../../docs/protocol.md")
	if err != nil {
		t.Fatal(err)
	}
	const opening = "```trace-schema\n"
	start := strings.Index(string(document), opening)
	if start < 0 {
		t.Fatal("docs/protocol.md has no trace-schema table")
	}
	remainder := string(document)[start+len(opening):]
	end := strings.Index(remainder, "\n```")
	if end < 0 {
		t.Fatal("docs/protocol.md has unterminated trace-schema table")
	}
	records, err := csv.NewReader(strings.NewReader(remainder[:end])).ReadAll()
	if err != nil {
		t.Fatalf("parse trace-schema table: %v", err)
	}
	rows := make([]string, 0, len(records))
	for _, record := range records {
		rows = append(rows, strings.Join(record, ","))
	}
	if len(rows) > 1 {
		sort.Strings(rows[1:])
	}
	return rows
}

func uniqueSorted(values []string) []string {
	sort.Strings(values)
	result := values[:0]
	for _, value := range values {
		if len(result) == 0 || result[len(result)-1] != value {
			result = append(result, value)
		}
	}
	return result
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
