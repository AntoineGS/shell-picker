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

const TraceSchema = 1

type TraceEvent struct {
	Name           string
	Generation     uint64
	CandidateCount int
	Renderer       string
	Outcome        string
	Path           []byte
}

type TraceRecord struct {
	Schema         int    `json:"schema"`
	Time           string `json:"time"`
	Session        string `json:"session"`
	Event          string `json:"event"`
	Generation     uint64 `json:"generation,omitempty"`
	CandidateCount int    `json:"candidate_count,omitempty"`
	Renderer       string `json:"renderer,omitempty"`
	Outcome        string `json:"outcome,omitempty"`
	Path           string `json:"path,omitempty"`
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
	record := TraceRecord{Schema: TraceSchema, Time: time.Now().UTC().Format(time.RFC3339Nano), Session: trace.session,
		Event: event.Name, Generation: event.Generation, CandidateCount: event.CandidateCount,
		Renderer: event.Renderer, Outcome: event.Outcome}
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
	if event.Generation > 0 && event.Name != "generation.publish" && event.Name != "callback.load" {
		return errors.New("trace: generation is not valid for event")
	}
	if event.CandidateCount < 0 || event.CandidateCount > 1_000_000 ||
		event.CandidateCount != 0 && event.Name != "generation.publish" {
		return errors.New("trace: invalid candidate count")
	}
	if event.Path != nil && event.Name != "generation.publish" {
		return errors.New("trace: path is not valid for event")
	}
	if event.Renderer != "" && event.Name != "preview.dispatch" {
		return errors.New("trace: renderer is not valid for event")
	}
	if event.Name == "preview.dispatch" && !validRenderer(event.Renderer) {
		return errors.New("trace: invalid renderer")
	}
	if !validTraceOutcome(event.Name, event.Outcome) {
		return errors.New("trace: invalid event or outcome")
	}
	return nil
}

func validTraceOutcome(event, outcome string) bool {
	allowed := map[string]map[string]bool{
		"session.start":      {"cd": true, "cp": true},
		"generation.publish": {"ok": true},
		"fzf.start":          {"ok": true},
		"fzf.exit":           {"ok": true, "aborted": true, "error": true},
		"callback.event":     {"mi": true, "ma": true, "es": true, "fw": true, "up": true, "sl": true, "hm": true, "en": true},
		"callback.load":      {"ok": true, "error": true},
		"preview.dispatch":   {"ok": true, "error": true},
		"session.close":      {"accepted": true, "aborted": true, "error": true},
	}
	return allowed[event][outcome]
}

func validRenderer(renderer string) bool {
	allowed := map[string]bool{
		"native": true, "eza": true, "glow": true, "bat": true, "kitten": true, "chafa": true,
		"unzip": true, "gzip": true, "xz": true, "tar": true, "file": true, "pdftoppm": true,
		"ffmpegthumbnailer": true, "ffmpeg": true, "exiftool": true,
	}
	return allowed[renderer]
}
