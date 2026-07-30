package integration

import (
	"encoding/csv"
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

func TestTraceContractChangesWithValidationAuthority(t *testing.T) {
	base := traceSchemaAuthority()
	baseline := strings.Join(traceContractRowsForSchema(t, base), "\n")
	tests := []struct {
		name   string
		mutate func(*traceSchemaRules)
		want   string
		reject string
	}{
		{"add event outcome", func(schema *traceSchemaRules) {
			schema.EventOutcomes["fzf.exit"] = append(schema.EventOutcomes["fzf.exit"], "interrupted")
		}, "interrupted", ""},
		{"add zoxide policy", func(schema *traceSchemaRules) {
			schema.ZoxidePolicies = append(schema.ZoxidePolicies, "offline")
		}, "offline", ""},
		{"remove zoxide outcome", func(schema *traceSchemaRules) {
			schema.ZoxideOutcomes = removeString(schema.ZoxideOutcomes, "timeout")
		}, "@zoxide_outcomes", "timeout"},
		{"change candidate count maximum", func(schema *traceSchemaRules) {
			schema.CandidateCountMax++
		}, "candidate_count=0..1000001", ""},
		{"change duration maximum", func(schema *traceSchemaRules) {
			schema.DurationMax -= time.Microsecond
		}, "duration_us=0..86399999999", ""},
		{"change timestamp past bound", func(schema *traceSchemaRules) {
			schema.TimestampPastLimit -= time.Hour
		}, "timestamp_input=zero-or-now-23h0m0s..now+1s", ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			schema := cloneTraceSchemaRules(base)
			test.mutate(&schema)
			contract := strings.Join(traceContractRowsForSchema(t, schema), "\n")
			if contract == baseline {
				t.Fatal("authority mutation did not change generated trace contract")
			}
			if !strings.Contains(contract, test.want) {
				t.Fatalf("mutated contract does not contain %q:\n%s", test.want, contract)
			}
			if test.reject != "" && strings.Contains(contract, test.reject) {
				t.Fatalf("mutated contract still contains removed authority value %q:\n%s", test.reject, contract)
			}
		})
	}
}

func TestTraceValidationUsesAuthoritativeZoxideVocabulary(t *testing.T) {
	schema := cloneTraceSchemaRules(traceSchemaAuthority())
	schema.ZoxidePolicies = append(schema.ZoxidePolicies, "offline")
	schema.ZoxideOutcomes = append(schema.ZoxideOutcomes, "deferred")
	event := TraceEvent{Name: "generation.publish", Outcome: "ok", ZoxidePolicy: "offline", ZoxideOutcome: "deferred"}
	if err := validateTraceEventWithSchema(event, schema, time.Now()); err != nil {
		t.Fatalf("schema-authorized zoxide vocabulary rejected: %v", err)
	}
}

func cloneTraceSchemaRules(schema traceSchemaRules) traceSchemaRules {
	clone := schema
	clone.EventOutcomes = make(map[string][]string, len(schema.EventOutcomes))
	for event, outcomes := range schema.EventOutcomes {
		clone.EventOutcomes[event] = append([]string(nil), outcomes...)
	}
	clone.RendererBases = append([]string(nil), schema.RendererBases...)
	clone.ZoxidePolicies = append([]string(nil), schema.ZoxidePolicies...)
	clone.ZoxideOutcomes = append([]string(nil), schema.ZoxideOutcomes...)
	return clone
}

func removeString(values []string, remove string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != remove {
			result = append(result, value)
		}
	}
	return result
}

func traceContractRows(t *testing.T) []string {
	return traceContractRowsForSchema(t, traceSchemaAuthority())
}

func traceContractRowsForSchema(t *testing.T, schema traceSchemaRules) []string {
	renderers := []string{}
	for _, candidate := range rendererStringCandidates(schema.RendererBases) {
		if validRendererWithSchema(candidate, schema) {
			renderers = append(renderers, candidate)
		}
	}
	renderers = uniqueSorted(renderers)

	rows := []string{
		"event,outcomes,generation,renderer,counters,optional_fields",
		"@numeric_bounds," + strings.Join(traceNumericBounds(schema), "|") + ",,,,",
		"@renderers," + strings.Join(renderers, "|") + ",,,,",
		"@zoxide_outcomes," + strings.Join(uniqueSorted(append([]string(nil), schema.ZoxideOutcomes...)), "|") + ",,,,",
		"@zoxide_policies," + strings.Join(uniqueSorted(append([]string(nil), schema.ZoxidePolicies...)), "|") + ",,,,",
	}
	for event, acceptedOutcomes := range schema.EventOutcomes {
		acceptedOutcomes = uniqueSorted(append([]string(nil), acceptedOutcomes...))
		profiles := map[string][]string{}
		for _, outcome := range acceptedOutcomes {
			base := TraceEvent{Name: event, Outcome: outcome}
			acceptedRenderers := []string{}
			if validateTraceEventWithSchema(base, schema, time.Now()) == nil {
				acceptedRenderers = append(acceptedRenderers, "")
			}
			for _, renderer := range renderers {
				candidate := base
				candidate.Renderer = renderer
				if validateTraceEventWithSchema(candidate, schema, time.Now()) == nil {
					acceptedRenderers = append(acceptedRenderers, renderer)
				}
			}
			if len(acceptedRenderers) == 0 {
				t.Fatalf("event %q outcome %q has no validator-accepted renderer state", event, outcome)
			}
			base.Renderer = acceptedRenderers[0]
			profile := strings.Join([]string{
				acceptedGenerationStates(t, base, schema),
				documentedRendererState(acceptedRenderers, renderers),
				acceptedCounterStates(t, base, acceptedRenderers, schema),
				strings.Join(acceptedOptionalFields(base, schema), "|"),
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
		if base != "native" {
			candidates = append(candidates, base+"-fallback")
		}
	}
	return uniqueSorted(candidates)
}

func traceNumericBounds(schema traceSchemaRules) []string {
	timestampRequirement := "optional"
	if schema.TimestampRequired {
		timestampRequirement = "required"
	}
	timestampZone := "offset-preserved"
	if schema.TimestampMustBeUTC {
		timestampZone = "utc"
	}
	bounds := []string{
		"schema=" + strconv.Itoa(TraceSchema),
		"generation=0.." + strconv.FormatUint(schema.GenerationMax, 10),
		"candidate_count=" + strconv.Itoa(schema.CandidateCountMin) + ".." + strconv.Itoa(schema.CandidateCountMax),
		"child_starts=" + strconv.Itoa(schema.ChildStartsMin) + ".." + strconv.Itoa(schema.ChildStartsMax),
		"max_live_children=" + strconv.Itoa(schema.MaxLiveChildrenMin) + ".." + strconv.Itoa(schema.MaxLiveChildrenMax) + "<=child_starts",
		"zoxide_attempts=" + strconv.Itoa(schema.ZoxideCounterMin) + ".." + strconv.Itoa(schema.ZoxideAttemptsMax),
		"zoxide_starts=" + strconv.Itoa(schema.ZoxideCounterMin) + "..zoxide_attempts",
		"zoxide_exits=zoxide_starts",
		"zoxide_processes=zoxide_starts",
		"zoxide_live=" + strconv.Itoa(schema.ZoxideLiveRequired),
		"zoxide_max_live=" + strconv.Itoa(schema.ZoxideCounterMin) + "..zoxide_starts",
		"duration_us=" + strconv.FormatInt(schema.DurationMin.Microseconds(), 10) + ".." + strconv.FormatInt(schema.DurationMax.Microseconds(), 10),
		"timestamp=" + timestampRequirement + "-" + schema.TimestampFormat + "-" + timestampZone,
		"timestamp_input=zero-or-now-" + schema.TimestampPastLimit.String() + "..now+" + schema.TimestampFutureLimit.String(),
	}
	return uniqueSorted(bounds)
}

func acceptedGenerationStates(t *testing.T, base TraceEvent, schema traceSchemaRules) string {
	t.Helper()
	states := []string{}
	for _, generation := range []uint64{0, 1} {
		candidate := base
		candidate.Generation = generation
		if validateTraceEventWithSchema(candidate, schema, time.Now()) == nil {
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

func acceptedCounterStates(t *testing.T, base TraceEvent, renderers []string, schema traceSchemaRules) string {
	t.Helper()
	groups := map[string][]string{}
	for _, renderer := range renderers {
		states := []string{}
		for childStarts := schema.ChildStartsMin - 1; childStarts <= schema.ChildStartsMax+1; childStarts++ {
			for maxLive := schema.MaxLiveChildrenMin - 1; maxLive <= schema.MaxLiveChildrenMax+1; maxLive++ {
				candidate := base
				candidate.Renderer = renderer
				candidate.ChildStarts = childStarts
				candidate.MaxLiveChildren = maxLive
				if validateTraceEventWithSchema(candidate, schema, time.Now()) == nil {
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

func acceptedOptionalFields(base TraceEvent, schema traceSchemaRules) []string {
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
		if validateTraceEventWithSchema(candidate, schema, time.Now()) == nil {
			accepted = append(accepted, name)
		}
	}
	if states := acceptedZoxideRequiredFields(base, schema); len(states) != 0 {
		accepted = append(accepted, "zoxide_*:"+strings.Join(states, "|"))
	}
	sort.Strings(accepted)
	return accepted
}

func acceptedZoxideRequiredFields(base TraceEvent, schema traceSchemaRules) []string {
	if len(schema.ZoxidePolicies) == 0 || len(schema.ZoxideOutcomes) == 0 {
		return nil
	}
	states := []string{}
	for mask := 1; mask < 4; mask++ {
		candidate := base
		parts := []string{}
		if mask&1 != 0 {
			candidate.ZoxidePolicy = schema.ZoxidePolicies[0]
			parts = append(parts, "policy")
		}
		if mask&2 != 0 {
			candidate.ZoxideOutcome = schema.ZoxideOutcomes[0]
			parts = append(parts, "outcome")
		}
		if validateTraceEventWithSchema(candidate, schema, time.Now()) == nil {
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
