package integration

import (
	"encoding/binary"
	"encoding/json"
	"io"
)

type controlEvent struct {
	Event   string `json:"event"`
	Nonce   string `json:"nonce,omitempty"`
	PID     int    `json:"pid"`
	Columns int    `json:"columns,omitempty"`
	Lines   int    `json:"lines,omitempty"`
}

func readControlFrame(reader io.Reader, destination *controlEvent) error {
	var size uint32
	if err := binary.Read(reader, binary.BigEndian, &size); err != nil {
		return err
	}
	if size == 0 || size > 4096 {
		return io.ErrUnexpectedEOF
	}
	payload := make([]byte, size)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return err
	}
	return json.Unmarshal(payload, destination)
}

func writeControlFrame(writer io.Writer, event controlEvent) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if err := binary.Write(writer, binary.BigEndian, uint32(len(payload))); err != nil {
		return err
	}
	_, err = writer.Write(payload)
	return err
}
