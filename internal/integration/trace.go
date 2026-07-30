package integration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

const TraceSchema = 1

type TraceEvent struct {
	Name              string
	Generation        uint64
	CandidateCount    int
	Renderer          string
	ChildStarts       int
	MaxLiveChildren   int
	Outcome           string
	Path              []byte
	ZoxidePolicy      string
	ZoxideAttempts    int
	ZoxideStarts      int
	ZoxideExits       int
	ZoxideProcesses   int
	ZoxideLive        int
	ZoxideMaxLive     int
	ActorQueueWait    time.Duration
	CallbackIPC       time.Duration
	LocalDuration     time.Duration
	ZoxideDuration    time.Duration
	ZoxideOutcome     string
	TransformDuration time.Duration
	LoadDuration      time.Duration
	Timestamp         time.Time
}

type TraceRecord struct {
	Schema           int    `json:"schema"`
	Time             string `json:"time"`
	Session          string `json:"session"`
	Event            string `json:"event"`
	Generation       uint64 `json:"generation"`
	CandidateCount   int    `json:"candidate_count"`
	Renderer         string `json:"renderer"`
	ChildStarts      int    `json:"child_starts"`
	MaxLiveChildren  int    `json:"max_live_children"`
	Outcome          string `json:"outcome"`
	Path             string `json:"path,omitempty"`
	ZoxidePolicy     string `json:"zoxide_policy,omitempty"`
	ZoxideAttempts   int    `json:"zoxide_attempts,omitempty"`
	ZoxideStarts     int    `json:"zoxide_starts,omitempty"`
	ZoxideExits      int    `json:"zoxide_exits,omitempty"`
	ZoxideProcesses  int    `json:"zoxide_processes,omitempty"`
	ZoxideLive       int    `json:"zoxide_live,omitempty"`
	ZoxideMaxLive    int    `json:"zoxide_max_live,omitempty"`
	ActorQueueWaitUS int64  `json:"actor_queue_wait_us,omitempty"`
	CallbackIPCUS    int64  `json:"callback_ipc_us,omitempty"`
	LocalUS          int64  `json:"local_us,omitempty"`
	ZoxideUS         int64  `json:"zoxide_us,omitempty"`
	ZoxideOutcome    string `json:"zoxide_outcome,omitempty"`
	TransformUS      int64  `json:"transform_us,omitempty"`
	LoadUS           int64  `json:"load_us,omitempty"`
}

type Trace struct {
	mu       sync.Mutex
	writer   io.Writer
	session  string
	disabled bool
}

func NewTrace(writer io.Writer, sessionID [16]byte) *Trace {
	return &Trace{writer: writer, session: redact(sessionID[:])}
}

func (trace *Trace) Event(event TraceEvent) error {
	if trace == nil {
		return errors.New("trace: nil trace")
	}
	if err := validateTraceEvent(event); err != nil {
		return err
	}
	schema := traceSchemaAuthority()
	timestamp := event.Timestamp
	if timestamp.IsZero() && schema.TimestampRequired {
		timestamp = time.Now()
	}
	timestampText := ""
	if !timestamp.IsZero() {
		if schema.TimestampMustBeUTC {
			timestamp = timestamp.UTC()
		}
		timestampText = timestamp.Format(traceTimestampLayout(schema.TimestampFormat))
	}
	record := TraceRecord{Schema: TraceSchema, Time: timestampText, Session: trace.session,
		Event: event.Name, Generation: event.Generation, CandidateCount: event.CandidateCount,
		Renderer: event.Renderer, ChildStarts: event.ChildStarts, MaxLiveChildren: event.MaxLiveChildren, Outcome: event.Outcome,
		ZoxidePolicy: event.ZoxidePolicy, ZoxideAttempts: event.ZoxideAttempts, ZoxideStarts: event.ZoxideStarts,
		ZoxideExits: event.ZoxideExits, ZoxideProcesses: event.ZoxideProcesses, ZoxideLive: event.ZoxideLive,
		ZoxideMaxLive: event.ZoxideMaxLive, ActorQueueWaitUS: event.ActorQueueWait.Microseconds(),
		CallbackIPCUS: event.CallbackIPC.Microseconds(), LocalUS: event.LocalDuration.Microseconds(),
		ZoxideUS: event.ZoxideDuration.Microseconds(), ZoxideOutcome: event.ZoxideOutcome,
		TransformUS: event.TransformDuration.Microseconds(), LoadUS: event.LoadDuration.Microseconds()}
	if event.Path != nil {
		record.Path = redact(event.Path)
	}
	line, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("trace: encode event: %w", err)
	}
	line = append(line, '\n')

	trace.mu.Lock()
	defer trace.mu.Unlock()
	if trace.disabled {
		return nil
	}
	if trace.writer == nil {
		trace.disabled = true
		return errors.New("trace: nil writer")
	}
	written, err := trace.writer.Write(line)
	if err == nil && written != len(line) {
		err = io.ErrShortWrite
	}
	if err != nil {
		trace.disabled = true
		return fmt.Errorf("trace: write event: %w", err)
	}
	return nil
}

func redact(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:8])
}

func validateTraceEvent(event TraceEvent) error {
	return validateTraceEventWithSchema(event, traceSchemaAuthority(), time.Now())
}

func validateTraceEventWithSchema(event TraceEvent, schema traceSchemaRules, now time.Time) error {
	if !event.Timestamp.IsZero() {
		if event.Timestamp.After(now.Add(schema.TimestampFutureLimit)) ||
			event.Timestamp.Before(now.Add(-schema.TimestampPastLimit)) {
			return errors.New("trace: invalid timestamp")
		}
	}
	if event.Generation > 0 && event.Name != "generation.start" && event.Name != "generation.publish" &&
		event.Name != "generation.discard" && event.Name != "callback.load" {
		return errors.New("trace: generation is not valid for event")
	}
	if event.CandidateCount < schema.CandidateCountMin || event.CandidateCount > schema.CandidateCountMax ||
		event.CandidateCount != 0 && event.Name != "generation.publish" {
		return errors.New("trace: invalid candidate count")
	}
	if event.Path != nil && event.Name != "generation.publish" {
		return errors.New("trace: path is not valid for event")
	}
	if event.Renderer != "" && event.Name != "preview.dispatch" && event.Name != "preview.finished" {
		if event.Name != "preview.cancel" && event.Name != "preview.exit" {
			return errors.New("trace: renderer is not valid for event")
		}
	}
	if isPreviewEvent(event.Name) && !validRendererWithSchema(event.Renderer, schema) {
		return errors.New("trace: invalid renderer")
	}
	if event.ChildStarts < schema.ChildStartsMin || event.ChildStarts > schema.ChildStartsMax ||
		event.MaxLiveChildren < schema.MaxLiveChildrenMin || event.MaxLiveChildren > schema.MaxLiveChildrenMax ||
		event.MaxLiveChildren > event.ChildStarts {
		return errors.New("trace: invalid preview child counters")
	}
	if event.ChildStarts != 0 || event.MaxLiveChildren != 0 {
		if event.Name != "preview.finished" && event.Name != "preview.exit" {
			return errors.New("trace: preview child counters are not valid for event")
		}
	}
	if event.Renderer == "native" && (event.ChildStarts != 0 || event.MaxLiveChildren != 0) {
		return errors.New("trace: native preview cannot have child counters")
	}
	if err := validateZoxideFields(event, schema); err != nil {
		return err
	}
	if err := validateTimingFields(event, schema); err != nil {
		return err
	}
	if !validTraceOutcomeWithSchema(event.Name, event.Outcome, schema) {
		return errors.New("trace: invalid event or outcome")
	}
	return nil
}

func isPreviewEvent(name string) bool {
	return name == "preview.dispatch" || name == "preview.finished" || name == "preview.cancel" || name == "preview.exit"
}

func validateZoxideFields(event TraceEvent, schema traceSchemaRules) error {
	if event.ZoxideAttempts < schema.ZoxideCounterMin || event.ZoxideStarts < schema.ZoxideCounterMin ||
		event.ZoxideExits < schema.ZoxideCounterMin || event.ZoxideProcesses < schema.ZoxideCounterMin ||
		event.ZoxideLive < schema.ZoxideCounterMin || event.ZoxideMaxLive < schema.ZoxideCounterMin ||
		event.ZoxideAttempts > schema.ZoxideAttemptsMax ||
		event.ZoxideStarts > event.ZoxideAttempts || event.ZoxideExits != event.ZoxideStarts ||
		event.ZoxideProcesses != event.ZoxideStarts || event.ZoxideLive != schema.ZoxideLiveRequired ||
		event.ZoxideMaxLive > event.ZoxideStarts {
		return errors.New("trace: invalid zoxide counters")
	}
	hasFields := event.ZoxidePolicy != "" || event.ZoxideAttempts != 0 || event.ZoxideStarts != 0 || event.ZoxideExits != 0 ||
		event.ZoxideProcesses != 0 || event.ZoxideLive != 0 ||
		event.ZoxideMaxLive != 0 || event.ZoxideDuration != 0 || event.ZoxideOutcome != ""
	if hasFields && event.Name != "generation.publish" && event.Name != "generation.discard" {
		return errors.New("trace: zoxide fields are not valid for event")
	}
	if hasFields && (event.ZoxidePolicy == "" || event.ZoxideOutcome == "") {
		return errors.New("trace: incomplete zoxide fields")
	}
	if event.ZoxidePolicy != "" && !schemaContains(schema.ZoxidePolicies, event.ZoxidePolicy) {
		return errors.New("trace: invalid zoxide policy")
	}
	if event.ZoxideOutcome != "" && !schemaContains(schema.ZoxideOutcomes, event.ZoxideOutcome) {
		return errors.New("trace: invalid zoxide outcome")
	}
	return nil
}

func validateTimingFields(event TraceEvent, schema traceSchemaRules) error {
	durations := []time.Duration{event.ActorQueueWait, event.CallbackIPC, event.LocalDuration, event.ZoxideDuration,
		event.TransformDuration, event.LoadDuration}
	for _, duration := range durations {
		if duration < schema.DurationMin || duration > schema.DurationMax {
			return errors.New("trace: invalid duration")
		}
	}
	if (event.ActorQueueWait != 0 || event.LocalDuration != 0 || event.TransformDuration != 0) &&
		event.Name != "generation.publish" && event.Name != "generation.discard" {
		return errors.New("trace: generation timing is not valid for event")
	}
	if event.CallbackIPC != 0 && event.Name != "callback.event" {
		return errors.New("trace: callback timing is not valid for event")
	}
	if event.LoadDuration != 0 && event.Name != "callback.load" {
		return errors.New("trace: load timing is not valid for event")
	}
	return nil
}

func validTraceOutcomeWithSchema(event, outcome string, schema traceSchemaRules) bool {
	return schemaContains(schema.EventOutcomes[event], outcome)
}

func validRendererWithSchema(renderer string, schema traceSchemaRules) bool {
	if schemaContains(schema.RendererBases, renderer) {
		return true
	}
	base, fallback := strings.CutSuffix(renderer, "-fallback")
	return fallback && base != "native" && schemaContains(schema.RendererBases, base)
}
