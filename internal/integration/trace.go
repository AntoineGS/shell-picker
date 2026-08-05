package integration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

const TraceSchema = 2

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
	SidecarAttempt    uint64
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
	SidecarAttempt   uint64 `json:"sidecar_attempt,omitempty"`
	TransformUS      int64  `json:"transform_us,omitempty"`
	LoadUS           int64  `json:"load_us,omitempty"`
}

type Trace struct {
	mu       sync.Mutex
	writer   io.Writer
	session  string
	disabled bool
}

// RecordWriter writes one complete JSONL record atomically. Implementations
// own any cross-process locking and all partial-write retries.
type RecordWriter interface {
	WriteRecord([]byte) error
}

func NewTrace(writer io.Writer, sessionID [16]byte) *Trace {
	return &Trace{writer: writer, session: RedactedSessionID(sessionID)}
}

// NewTraceWithRedactedSession constructs a trace writer from the bounded
// session value already safe for JSONL output.
func NewTraceWithRedactedSession(writer io.Writer, session string) (*Trace, error) {
	if !validTraceRedaction(session) {
		return nil, errors.New("trace: invalid redacted session")
	}
	return &Trace{writer: writer, session: session}, nil
}

// RedactedSessionID returns the bounded, non-reversible session identifier
// written to JSONL trace records.
func RedactedSessionID(sessionID [16]byte) string {
	return redact(sessionID[:])
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
		SidecarAttempt: event.SidecarAttempt,
		TransformUS:    event.TransformDuration.Microseconds(), LoadUS: event.LoadDuration.Microseconds()}
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
	if writer, ok := trace.writer.(RecordWriter); ok {
		if err := writer.WriteRecord(line); err != nil {
			trace.disabled = true
			return fmt.Errorf("trace: write event: %w", err)
		}
		return nil
	}
	for len(line) > 0 {
		written, err := trace.writer.Write(line)
		if written < 0 || written > len(line) {
			trace.disabled = true
			return fmt.Errorf("trace: write event: invalid byte count %d", written)
		}
		if written > 0 {
			line = line[written:]
		}
		if err != nil {
			// os.File.Write reports io.ErrShortWrite when the underlying
			// writer accepted a prefix. Keep the JSONL record atomic by
			// retrying the unwritten suffix while the trace lock is held.
			if errors.Is(err, io.ErrShortWrite) && written > 0 && len(line) > 0 {
				continue
			}
			trace.disabled = true
			return fmt.Errorf("trace: write event: %w", err)
		}
		if written == 0 {
			trace.disabled = true
			return fmt.Errorf("trace: write event: %w", io.ErrShortWrite)
		}
	}
	return nil
}

// Close serializes sink closure with Event so an in-flight JSONL record cannot
// be truncated by teardown.
func (trace *Trace) Close() error {
	if trace == nil {
		return nil
	}
	trace.mu.Lock()
	defer trace.mu.Unlock()
	if trace.writer == nil {
		return nil
	}
	closer, ok := trace.writer.(io.Closer)
	trace.disabled = true
	trace.writer = nil
	if !ok {
		return nil
	}
	return closer.Close()
}

func redact(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:8])
}
