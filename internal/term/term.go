package term

import (
	"context"
	"fmt"
	"sync"

	"github.com/famgia/remote-host/internal/channel"
	"github.com/famgia/remote-host/internal/protocol"
)

// Handler is the per-connection adapter to the shared session Manager. It
// implements channel.Handler and channel.BinaryHandler.
//
// Message types (text envelopes on ChTerm), correlated by e.ID:
//
//	list                              → "list"     {sessions:[SessionInfo]}
//	create  {cols,rows,title?}        → "created"  {sessionId,title} + scrollback
//	attach  {sessionId,cols?,rows?}   → "attached" {sessionId}        + scrollback
//	detach  {sessionId}               → "detached" {sessionId}   (session stays alive)
//	resize  {sessionId,cols,rows}
//	kill    {sessionId}               → "killed"   {sessionId}   (terminates the shell)
//
// Binary frames: BinTermInput (keystrokes) and BinTermOutput (output), keyed by
// the sessionId in StreamID. A session's exit is announced with "exit"
// {sessionId} to every attached connection.
//
// On connection close the Handler DETACHES from every session it attached to —
// it never kills them, so they keep running for a later re-attach.
type Handler struct {
	mgr *Manager

	mu       sync.Mutex
	attached map[string]int // sessionId → this connection's subscriber id
}

var _ channel.Handler = (*Handler)(nil)
var _ channel.BinaryHandler = (*Handler)(nil)

// New returns a per-connection Handler bound to the shared session Manager.
func New(mgr *Manager) *Handler {
	return &Handler{mgr: mgr, attached: make(map[string]int)}
}

// Channel returns the channel this handler serves.
func (h *Handler) Channel() protocol.Channel { return protocol.ChTerm }

// ---- message payloads -------------------------------------------------------

type createData struct {
	Cols  uint16 `json:"cols"`
	Rows  uint16 `json:"rows"`
	Title string `json:"title"`
}

type attachData struct {
	SessionID string `json:"sessionId"`
	Cols      uint16 `json:"cols"`
	Rows      uint16 `json:"rows"`
}

type sessionRef struct {
	SessionID string `json:"sessionId"`
}

type resizeData struct {
	SessionID string `json:"sessionId"`
	Cols      uint16 `json:"cols"`
	Rows      uint16 `json:"rows"`
}

// connSub forwards a session's output/exit to one WebSocket connection.
type connSub struct {
	c         channel.Conn
	sessionID string
}

func (cs *connSub) Output(payload []byte) {
	_ = cs.c.SendBinary(protocol.BinaryFrame{
		Kind:     protocol.BinTermOutput,
		StreamID: cs.sessionID,
		Payload:  payload,
	})
}

func (cs *connSub) Exit() {
	env, err := protocol.NewEnvelope(protocol.ChTerm, "exit", "", sessionRef{SessionID: cs.sessionID})
	if err == nil {
		_ = cs.c.Send(env)
	}
}

// ---- Handle -----------------------------------------------------------------

func (h *Handler) Handle(_ context.Context, e protocol.Envelope, c channel.Conn) error {
	switch e.Type {
	case "list":
		return h.list(e, c)
	case "create":
		return h.create(e, c)
	case "attach":
		return h.attach(e, c)
	case "detach", "close": // "close" kept as a detach alias for older clients
		return h.detach(e, c)
	case "resize":
		return h.resize(e)
	case "kill":
		return h.kill(e, c)
	default:
		return fmt.Errorf("term: unknown type %q", e.Type)
	}
}

func (h *Handler) list(e protocol.Envelope, c channel.Conn) error {
	env, err := protocol.NewEnvelope(protocol.ChTerm, "list", e.ID,
		map[string]any{"sessions": h.mgr.List()})
	if err != nil {
		return err
	}
	return c.Send(env)
}

func (h *Handler) create(e protocol.Envelope, c channel.Conn) error {
	var d createData
	if err := e.Bind(&d); err != nil {
		return fmt.Errorf("term: create: %w", err)
	}
	s, err := h.mgr.Create(c.Profile(), d.Cols, d.Rows, d.Title)
	if err != nil {
		return err
	}
	h.bind(s, c)
	env, err := protocol.NewEnvelope(protocol.ChTerm, "created", e.ID,
		map[string]any{"sessionId": s.ID(), "title": s.Info().Title})
	if err != nil {
		return err
	}
	return c.Send(env)
}

func (h *Handler) attach(e protocol.Envelope, c channel.Conn) error {
	var d attachData
	if err := e.Bind(&d); err != nil {
		return fmt.Errorf("term: attach: %w", err)
	}
	// Attach revives the session from disk (re-attaching a dtach client) if it
	// isn't in memory — e.g. after a hostd restart.
	s, err := h.mgr.Attach(c.Profile(), d.SessionID)
	if err != nil {
		return err
	}
	if d.Cols > 0 && d.Rows > 0 {
		_ = s.Resize(d.Cols, d.Rows)
	}
	scrollback := h.bind(s, c)
	// Replay recent output so the client picks up where the session was.
	if len(scrollback) > 0 {
		_ = c.SendBinary(protocol.BinaryFrame{
			Kind: protocol.BinTermOutput, StreamID: s.ID(), Payload: scrollback,
		})
	}
	env, err := protocol.NewEnvelope(protocol.ChTerm, "attached", e.ID,
		sessionRef{SessionID: s.ID()})
	if err != nil {
		return err
	}
	return c.Send(env)
}

// bind attaches this connection to s, replacing any prior attachment to the
// same session, and returns the scrollback to replay.
func (h *Handler) bind(s *Session, c channel.Conn) []byte {
	subID, scrollback := s.Attach(&connSub{c: c, sessionID: s.ID()})
	h.mu.Lock()
	if old, exists := h.attached[s.ID()]; exists {
		s.Detach(old)
	}
	h.attached[s.ID()] = subID
	h.mu.Unlock()
	return scrollback
}

func (h *Handler) detach(e protocol.Envelope, c channel.Conn) error {
	var d sessionRef
	if err := e.Bind(&d); err != nil {
		return fmt.Errorf("term: detach: %w", err)
	}
	h.mu.Lock()
	subID, ok := h.attached[d.SessionID]
	if ok {
		delete(h.attached, d.SessionID)
	}
	h.mu.Unlock()
	if ok {
		if s, exists := h.mgr.Get(d.SessionID); exists {
			s.Detach(subID)
		}
	}
	env, err := protocol.NewEnvelope(protocol.ChTerm, "detached", e.ID, d)
	if err != nil {
		return err
	}
	return c.Send(env)
}

func (h *Handler) resize(e protocol.Envelope) error {
	var d resizeData
	if err := e.Bind(&d); err != nil {
		return fmt.Errorf("term: resize: %w", err)
	}
	s, ok := h.mgr.Get(d.SessionID)
	if !ok {
		return fmt.Errorf("term: resize: no such session %q", d.SessionID)
	}
	return s.Resize(d.Cols, d.Rows)
}

func (h *Handler) kill(e protocol.Envelope, c channel.Conn) error {
	var d sessionRef
	if err := e.Bind(&d); err != nil {
		return fmt.Errorf("term: kill: %w", err)
	}
	if err := h.mgr.Kill(d.SessionID); err != nil {
		return err
	}
	h.mu.Lock()
	delete(h.attached, d.SessionID)
	h.mu.Unlock()
	env, err := protocol.NewEnvelope(protocol.ChTerm, "killed", e.ID, d)
	if err != nil {
		return err
	}
	return c.Send(env)
}

// HandleBinary routes keystroke frames to the target session's PTY.
func (h *Handler) HandleBinary(_ context.Context, f protocol.BinaryFrame, _ channel.Conn) error {
	if f.Kind != protocol.BinTermInput {
		return nil
	}
	s, ok := h.mgr.Get(f.StreamID)
	if !ok {
		return fmt.Errorf("term: input: no such session %q", f.StreamID)
	}
	return s.Write(f.Payload)
}

// Close detaches this connection from every session it attached to. Sessions
// keep running (tmux-like) so another connection can re-attach later.
func (h *Handler) Close() error {
	h.mu.Lock()
	attached := h.attached
	h.attached = make(map[string]int)
	h.mu.Unlock()

	for id, subID := range attached {
		if s, ok := h.mgr.Get(id); ok {
			s.Detach(subID)
		}
	}
	return nil
}
