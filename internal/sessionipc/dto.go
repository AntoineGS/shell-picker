package sessionipc

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/AntoineGS/shell-picker/internal/protocol"
)

const (
	MaxPreviewChildStarts  = 3
	MaxPreviewLiveChildren = 1
)

type EventRequest struct {
	Opcode            protocol.Opcode `json:"opcode"`
	Key               string          `json:"key"`
	QueryBase64       string          `json:"query_base64"`
	CurrentItemBase64 string          `json:"current_item_base64"`
}

type EventResponse struct {
	Effect protocol.Effect `json:"effect"`
}

type LoadRequest struct {
	Generation uint64 `json:"generation"`
}

type PreviewRequest struct {
	Phase             string `json:"phase"`
	CurrentItemBase64 string `json:"current_item_base64"`
	Renderer          string `json:"renderer,omitempty"`
	DurationUS        int64  `json:"duration_us,omitempty"`
	ChildStarts       int    `json:"child_starts,omitempty"`
	MaxLiveChildren   int    `json:"max_live_children,omitempty"`
	Outcome           string `json:"outcome,omitempty"`
}

type PreviewResponse struct {
	Kind            protocol.Kind `json:"kind"`
	PathBase64      string        `json:"path_base64"`
	Size            int64         `json:"size"`
	ModTimeUnixNano int64         `json:"mod_time_unix_nano"`
	Mode            uint32        `json:"mode"`
}

type Backend interface {
	HandleEvent(context.Context, protocol.Event) (protocol.Effect, error)
	LoadGeneration(context.Context, uint64) ([]byte, error)
	ResolvePreview(context.Context, []byte) (protocol.ResolvedCandidate, error)
	RecordPreview(context.Context, PreviewRequest) error
}

func decodeObject(body []byte, destination any) error {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return errors.New("JSON value is not an object")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func decodeBytes(encoded string) ([]byte, error) {
	decoded, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || base64.StdEncoding.EncodeToString(decoded) != encoded {
		return nil, errors.New("invalid byte encoding")
	}
	return decoded, nil
}

func validatePreview(request PreviewRequest) error {
	if request.CurrentItemBase64 == "" {
		return errors.New("missing current item")
	}
	switch request.Phase {
	case "resolve":
		if request.Renderer != "" || request.DurationUS != 0 || request.ChildStarts != 0 || request.MaxLiveChildren != 0 || request.Outcome != "" {
			return errors.New("invalid resolve telemetry")
		}
	case "started":
		if !validTelemetryValue(request.Renderer) || request.DurationUS != 0 || request.ChildStarts != 0 || request.MaxLiveChildren != 0 || request.Outcome != "" {
			return errors.New("invalid started telemetry")
		}
	case "finished":
		if !validTelemetryValue(request.Renderer) || !validTelemetryValue(request.Outcome) || request.DurationUS < 0 ||
			request.DurationUS > int64(10*time.Second/time.Microsecond) || request.ChildStarts < 0 || request.ChildStarts > MaxPreviewChildStarts ||
			request.MaxLiveChildren < 0 || request.MaxLiveChildren > MaxPreviewLiveChildren || request.MaxLiveChildren > request.ChildStarts ||
			(request.Renderer == "native" && (request.ChildStarts != 0 || request.MaxLiveChildren != 0)) {
			return errors.New("invalid finished telemetry")
		}
	default:
		return errors.New("invalid preview phase")
	}
	return nil
}

func validTelemetryValue(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	for _, char := range []byte(value) {
		if char < 0x21 || char > 0x7e {
			return false
		}
	}
	return true
}
