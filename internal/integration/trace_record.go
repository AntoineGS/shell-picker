package integration

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// DecodeTraceRecord decodes one JSONL trace record, rejects unknown JSON
// fields, and validates it against the production trace schema.
func DecodeTraceRecord(data []byte) (TraceRecord, error) {
	return DecodeTraceRecordAt(data, time.Now())
}

// DecodeTraceRecordAt is DecodeTraceRecord with an injectable clock. A zero
// clock validates record shape and timestamp syntax without applying the
// freshness window.
func DecodeTraceRecordAt(data []byte, now time.Time) (TraceRecord, error) {
	var record TraceRecord
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return TraceRecord{}, fmt.Errorf("trace: decode record: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return TraceRecord{}, errors.New("trace: multiple JSON records")
		}
		return TraceRecord{}, fmt.Errorf("trace: trailing JSON: %w", err)
	}
	if err := ValidateTraceRecordAt(record, now); err != nil {
		return TraceRecord{}, err
	}
	return record, nil
}

// ValidateTraceRecord validates a decoded trace record with the current
// timestamp as its freshness reference.
func ValidateTraceRecord(record TraceRecord) error {
	return ValidateTraceRecordAt(record, time.Now())
}

// ValidateTraceRecordAt validates a decoded trace record against the same
// schema authority used when production emits trace records.
func ValidateTraceRecordAt(record TraceRecord, now time.Time) error {
	schema := traceSchemaAuthority()
	if record.Schema != TraceSchema {
		return fmt.Errorf("trace: unsupported schema %d", record.Schema)
	}
	if !validTraceRedaction(record.Session) {
		return errors.New("trace: invalid session redaction")
	}
	timestamp, err := parseTraceRecordTimestamp(record.Time, schema)
	if err != nil {
		return err
	}
	if record.Path != "" && !validTraceRedaction(record.Path) {
		return errors.New("trace: invalid path redaction")
	}
	actorQueueWait, err := traceRecordDuration(record.ActorQueueWaitUS, schema)
	if err != nil {
		return err
	}
	callbackIPC, err := traceRecordDuration(record.CallbackIPCUS, schema)
	if err != nil {
		return err
	}
	localDuration, err := traceRecordDuration(record.LocalUS, schema)
	if err != nil {
		return err
	}
	zoxideDuration, err := traceRecordDuration(record.ZoxideUS, schema)
	if err != nil {
		return err
	}
	transformDuration, err := traceRecordDuration(record.TransformUS, schema)
	if err != nil {
		return err
	}
	loadDuration, err := traceRecordDuration(record.LoadUS, schema)
	if err != nil {
		return err
	}
	event := TraceEvent{
		Name:              record.Event,
		Generation:        record.Generation,
		CandidateCount:    record.CandidateCount,
		Renderer:          record.Renderer,
		ChildStarts:       record.ChildStarts,
		MaxLiveChildren:   record.MaxLiveChildren,
		Outcome:           record.Outcome,
		ZoxidePolicy:      record.ZoxidePolicy,
		ZoxideAttempts:    record.ZoxideAttempts,
		ZoxideStarts:      record.ZoxideStarts,
		ZoxideExits:       record.ZoxideExits,
		ZoxideProcesses:   record.ZoxideProcesses,
		ZoxideLive:        record.ZoxideLive,
		ZoxideMaxLive:     record.ZoxideMaxLive,
		ActorQueueWait:    actorQueueWait,
		CallbackIPC:       callbackIPC,
		LocalDuration:     localDuration,
		ZoxideDuration:    zoxideDuration,
		ZoxideOutcome:     record.ZoxideOutcome,
		SidecarAttempt:    record.SidecarAttempt,
		TransformDuration: transformDuration,
		LoadDuration:      loadDuration,
		Timestamp:         timestamp,
	}
	if record.Path != "" {
		event.Path = []byte(record.Path)
	}
	if err := validateTraceEventWithSchema(event, schema, now); err != nil {
		return err
	}
	return nil
}

func parseTraceRecordTimestamp(value string, schema traceSchemaRules) (time.Time, error) {
	if value == "" {
		if schema.TimestampRequired {
			return time.Time{}, errors.New("trace: missing timestamp")
		}
		return time.Time{}, nil
	}
	if schema.TimestampMustBeUTC && !strings.HasSuffix(value, "Z") {
		return time.Time{}, errors.New("trace: timestamp is not UTC")
	}
	timestamp, err := time.Parse(traceTimestampLayout(schema.TimestampFormat), value)
	if err != nil {
		return time.Time{}, fmt.Errorf("trace: invalid timestamp: %w", err)
	}
	return timestamp, nil
}

func traceRecordDuration(value int64, schema traceSchemaRules) (time.Duration, error) {
	maxMicroseconds := int64(schema.DurationMax / time.Microsecond)
	if value < 0 || value > maxMicroseconds {
		return 0, errors.New("trace: invalid duration")
	}
	return time.Duration(value) * time.Microsecond, nil
}

func validTraceRedaction(value string) bool {
	const prefix = "sha256:"
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+16 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value[len(prefix):])
	return err == nil
}
