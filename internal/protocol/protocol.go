// Package protocol defines the wire format shared by hostd and the mobile app.
//
// A single WebSocket carries every channel. Two kinds of WS frames are used:
//
//   - Text frames  → JSON Envelope (control + request/response messages).
//   - Binary frames → BinaryFrame (high-throughput streams: terminal output,
//     file bytes) to avoid base64 overhead.
//
// The envelope is routed by its Ch (channel) field so new channels can be
// added without breaking older clients. See docs/PLAN.md for the design.
package protocol

import (
	"encoding/binary"
	"encoding/json"
	"errors"
)

// Channel identifies which subsystem an Envelope targets.
type Channel string

const (
	ChCtrl Channel = "ctrl" // pairing, auth, ping/pong, session lifecycle
	ChTerm Channel = "term" // PTY terminal
	ChFS   Channel = "fs"   // file operations
	ChSys  Channel = "sys"  // system metrics + process control
	ChAPI  Channel = "api"  // custom user-registered handlers
)

// Envelope is the JSON message sent over text WebSocket frames.
//
// ID correlates a response with its request (request/response channels such as
// fs and api). For streaming/event messages it may be empty.
type Envelope struct {
	Ch   Channel         `json:"ch"`
	Type string          `json:"type"`
	ID   string          `json:"id,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`
	Err  string          `json:"err,omitempty"`
}

// NewEnvelope builds an envelope, JSON-encoding data into Data.
func NewEnvelope(ch Channel, typ, id string, data any) (Envelope, error) {
	e := Envelope{Ch: ch, Type: typ, ID: id}
	if data != nil {
		raw, err := json.Marshal(data)
		if err != nil {
			return Envelope{}, err
		}
		e.Data = raw
	}
	return e, nil
}

// ErrorEnvelope builds an error response envelope for a given request id.
func ErrorEnvelope(ch Channel, typ, id, msg string) Envelope {
	return Envelope{Ch: ch, Type: typ, ID: id, Err: msg}
}

// Bind unmarshals the envelope's Data into v.
func (e Envelope) Bind(v any) error {
	if len(e.Data) == 0 {
		return nil
	}
	return json.Unmarshal(e.Data, v)
}

// ---- Binary frames ----------------------------------------------------------

// BinaryKind tags a binary frame so the receiver can route it.
type BinaryKind byte

const (
	BinTermOutput BinaryKind = 1 // PTY output:   StreamID = terminal session id
	BinTermInput  BinaryKind = 2 // PTY input:    StreamID = terminal session id
	BinFSData     BinaryKind = 3 // file bytes:   StreamID = transfer id
)

// BinaryFrame is the layout for binary WebSocket frames:
//
//	byte 0        : Kind
//	bytes 1..2    : StreamID length (uint16, big-endian)
//	bytes 3..3+n  : StreamID (UTF-8)
//	remaining     : Payload
//
// StreamID names the logical stream (terminal session id, transfer id, …) so a
// single WebSocket can multiplex many concurrent binary streams.
type BinaryFrame struct {
	Kind     BinaryKind
	StreamID string
	Payload  []byte
}

// ErrShortFrame is returned when a binary frame is truncated.
var ErrShortFrame = errors.New("protocol: short binary frame")

// Encode serializes the frame to bytes for a WebSocket binary message.
func (f BinaryFrame) Encode() []byte {
	idLen := len(f.StreamID)
	buf := make([]byte, 3+idLen+len(f.Payload))
	buf[0] = byte(f.Kind)
	binary.BigEndian.PutUint16(buf[1:3], uint16(idLen))
	copy(buf[3:3+idLen], f.StreamID)
	copy(buf[3+idLen:], f.Payload)
	return buf
}

// DecodeBinaryFrame parses bytes from a WebSocket binary message.
func DecodeBinaryFrame(b []byte) (BinaryFrame, error) {
	if len(b) < 3 {
		return BinaryFrame{}, ErrShortFrame
	}
	idLen := int(binary.BigEndian.Uint16(b[1:3]))
	if len(b) < 3+idLen {
		return BinaryFrame{}, ErrShortFrame
	}
	return BinaryFrame{
		Kind:     BinaryKind(b[0]),
		StreamID: string(b[3 : 3+idLen]),
		Payload:  append([]byte(nil), b[3+idLen:]...),
	}, nil
}
