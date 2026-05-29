// Package term implements the TERMINAL channel (protocol.ChTerm).
//
// One Handler instance is created per authenticated client connection. It
// manages a map of running PTY sessions, keyed by the sessionId chosen by
// the client. Inbound text envelopes drive session lifecycle (open/resize/close)
// while inbound binary frames (BinTermInput) feed keystrokes to the PTY; the
// PTY output is streamed back as BinTermOutput binary frames.
package term

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"github.com/famgia/remote-host/internal/channel"
	"github.com/famgia/remote-host/internal/launcher"
	"github.com/famgia/remote-host/internal/protocol"
)

// Handler handles the ChTerm channel for a single connection.
// It implements channel.Handler and channel.BinaryHandler.
//
// One Handler instance per connection is assumed; the server constructs a new
// Handler for each incoming authenticated WebSocket connection.
type Handler struct {
	launcher launcher.Launcher

	mu       sync.Mutex
	sessions map[string]launcher.Session // keyed by sessionId
}

// Compile-time interface assertions.
var _ channel.Handler = (*Handler)(nil)
var _ channel.BinaryHandler = (*Handler)(nil)

// New returns a Handler that uses l to spawn shell sessions.
func New(l launcher.Launcher) *Handler {
	return &Handler{
		launcher: l,
		sessions: make(map[string]launcher.Session),
	}
}

// Channel returns the channel this handler serves.
func (h *Handler) Channel() protocol.Channel { return protocol.ChTerm }

// ---- envelope data shapes ---------------------------------------------------

type openData struct {
	SessionID string `json:"sessionId"`
	Cols      uint16 `json:"cols"`
	Rows      uint16 `json:"rows"`
}

type resizeData struct {
	SessionID string `json:"sessionId"`
	Cols      uint16 `json:"cols"`
	Rows      uint16 `json:"rows"`
}

type closeData struct {
	SessionID string `json:"sessionId"`
}

type exitData struct {
	SessionID string `json:"sessionId"`
}

// ---- Handle -----------------------------------------------------------------

// Handle dispatches inbound text-frame envelopes on the term channel.
// Supported types: "open", "resize", "close".
func (h *Handler) Handle(ctx context.Context, e protocol.Envelope, c channel.Conn) error {
	switch e.Type {
	case "open":
		return h.handleOpen(ctx, e, c)
	case "resize":
		return h.handleResize(e)
	case "close":
		return h.handleClose(e)
	default:
		return fmt.Errorf("term: unknown envelope type %q", e.Type)
	}
}

// handleOpen launches a new shell session and starts copying PTY output to the client.
func (h *Handler) handleOpen(_ context.Context, e protocol.Envelope, c channel.Conn) error {
	var d openData
	if err := e.Bind(&d); err != nil {
		return fmt.Errorf("term: open: bad data: %w", err)
	}
	if d.SessionID == "" {
		return fmt.Errorf("term: open: sessionId required")
	}

	// Hold the lock across the existence check AND the store so two concurrent
	// opens with the same sessionId cannot both pass the check and leak a PTY
	// (the second store would otherwise silently overwrite — and orphan — the
	// first). StartShell is a fast fork+exec; holding the per-connection lock
	// for its duration only briefly serializes this one client's term ops.
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, exists := h.sessions[d.SessionID]; exists {
		return fmt.Errorf("term: open: session %q already exists", d.SessionID)
	}

	sess, err := h.launcher.StartShell(c.Profile(), launcher.Size{Cols: d.Cols, Rows: d.Rows})
	if err != nil {
		return fmt.Errorf("term: open: start shell: %w", err)
	}
	h.sessions[d.SessionID] = sess

	// Goroutine: copy PTY output → BinTermOutput frames, then send "exit".
	// It briefly blocks on h.mu (to delete the session on exit) until this
	// function returns and releases the lock.
	go h.pumpOutput(sess, d.SessionID, c)

	return nil
}

// pumpOutput reads PTY output and forwards it to the client as binary frames.
// When the PTY reaches EOF it sends an "exit" envelope.
func (h *Handler) pumpOutput(sess launcher.Session, sessionID string, c channel.Conn) {
	buf := make([]byte, 32*1024)
	for {
		n, err := sess.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			frame := protocol.BinaryFrame{
				Kind:     protocol.BinTermOutput,
				StreamID: sessionID,
				Payload:  chunk,
			}
			if sendErr := c.SendBinary(frame); sendErr != nil {
				// Connection is gone; stop pumping.
				break
			}
		}
		if err != nil {
			// EOF or error — shell exited.
			break
		}
	}

	// Wait for the process to exit so we get a clean exit status (ignore error).
	_ = sess.Wait()

	// Remove the session from the map.
	h.mu.Lock()
	delete(h.sessions, sessionID)
	h.mu.Unlock()

	// Notify the client that the session has ended.
	payload, _ := json.Marshal(exitData{SessionID: sessionID})
	env := protocol.Envelope{
		Ch:   protocol.ChTerm,
		Type: "exit",
		Data: json.RawMessage(payload),
	}
	_ = c.Send(env)
}

// handleResize forwards a window-size change to the running session.
func (h *Handler) handleResize(e protocol.Envelope) error {
	var d resizeData
	if err := e.Bind(&d); err != nil {
		return fmt.Errorf("term: resize: bad data: %w", err)
	}
	if d.SessionID == "" {
		return fmt.Errorf("term: resize: sessionId required")
	}

	h.mu.Lock()
	sess, ok := h.sessions[d.SessionID]
	h.mu.Unlock()
	if !ok {
		return fmt.Errorf("term: resize: session %q not found", d.SessionID)
	}
	return sess.Resize(launcher.Size{Cols: d.Cols, Rows: d.Rows})
}

// handleClose terminates and removes a session.
func (h *Handler) handleClose(e protocol.Envelope) error {
	var d closeData
	if err := e.Bind(&d); err != nil {
		return fmt.Errorf("term: close: bad data: %w", err)
	}
	if d.SessionID == "" {
		return fmt.Errorf("term: close: sessionId required")
	}
	return h.removeSession(d.SessionID)
}

// removeSession closes and deletes a session by id. Returns an error if the
// session does not exist.
func (h *Handler) removeSession(sessionID string) error {
	h.mu.Lock()
	sess, ok := h.sessions[sessionID]
	if ok {
		delete(h.sessions, sessionID)
	}
	h.mu.Unlock()

	if !ok {
		return fmt.Errorf("term: session %q not found", sessionID)
	}
	return sess.Close()
}

// ---- HandleBinary -----------------------------------------------------------

// HandleBinary routes BinTermInput frames to the appropriate PTY session.
func (h *Handler) HandleBinary(_ context.Context, f protocol.BinaryFrame, _ channel.Conn) error {
	if f.Kind != protocol.BinTermInput {
		// Not our kind; ignore silently.
		return nil
	}

	h.mu.Lock()
	sess, ok := h.sessions[f.StreamID]
	h.mu.Unlock()
	if !ok {
		return fmt.Errorf("term: binary input: session %q not found", f.StreamID)
	}

	_, err := io.Writer(sess).Write(f.Payload)
	if err != nil {
		return fmt.Errorf("term: binary input: write: %w", err)
	}
	return nil
}

// ---- Close ------------------------------------------------------------------

// Close terminates all sessions associated with this connection.
// Called by the server when the WebSocket connection goes away.
func (h *Handler) Close() error {
	h.mu.Lock()
	sessions := make(map[string]launcher.Session, len(h.sessions))
	for id, sess := range h.sessions {
		sessions[id] = sess
	}
	h.sessions = make(map[string]launcher.Session)
	h.mu.Unlock()

	var firstErr error
	for _, sess := range sessions {
		if err := sess.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
