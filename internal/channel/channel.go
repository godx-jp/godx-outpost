// Package channel defines the contract between the WebSocket server and the
// per-channel subsystems (term, fs, sys, api).
//
// The server owns the connection and the read loop. For each decoded message it
// routes to the Handler whose Channel() matches the envelope's Ch. Handlers push
// responses, events and binary streams back through the Conn they are given.
//
// This indirection lets the server package and the subsystem packages be built
// independently: they depend only on these interfaces, not on each other.
package channel

import (
	"context"

	"github.com/godx-jp/godx-outpost/internal/launcher"
	"github.com/godx-jp/godx-outpost/internal/protocol"
)

// Conn is the server-side view of a single authenticated client connection,
// handed to handlers so they can talk back to the client.
//
// Send and SendBinary are safe for concurrent use by multiple goroutines (a
// terminal session, for example, writes output from its own goroutine).
type Conn interface {
	// Send writes a JSON envelope as a text frame.
	Send(e protocol.Envelope) error
	// SendBinary writes a binary frame (terminal output, file bytes).
	SendBinary(f protocol.BinaryFrame) error
	// Profile returns the authenticated profile for this connection. Handlers
	// use it to scope behavior (admin vs sandboxed, fs root, …).
	Profile() launcher.Profile
}

// Handler processes envelopes for one channel.
//
// Handle is called once per inbound text-frame envelope addressed to this
// channel. It must not block the connection's read loop for long-running work;
// spawn a goroutine and stream results back through c instead. Returning an
// error causes the server to send an error envelope correlated to e.ID.
//
// Close is called once when the connection goes away, to release any per-
// connection resources (open PTYs, file transfers, metric tickers).
type Handler interface {
	Channel() protocol.Channel
	Handle(ctx context.Context, e protocol.Envelope, c Conn) error
	Close() error
}

// BinaryHandler is implemented by handlers that also consume binary frames
// (e.g. term receives keystroke input as protocol.BinTermInput frames). The
// server type-asserts for it when routing binary frames; the frame is routed to
// the handler whose Channel matches the frame's logical channel.
type BinaryHandler interface {
	HandleBinary(ctx context.Context, f protocol.BinaryFrame, c Conn) error
}
