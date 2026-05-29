// Package term implements the TERMINAL channel (protocol.ChTerm).
//
// Sessions are tmux-like: they are owned by a process-global Manager, not by
// the WebSocket connection that created them. A session keeps running after its
// client disconnects (the PTY stays open, output accumulates in a scrollback
// ring buffer), so a later connection can list sessions and re-attach — picking
// up where it left off. Sessions survive connection drops but NOT a host
// restart (the PTYs are in-process), mirroring tmux's detach semantics.
//
// manager.go holds the Manager and the managed Session; term.go holds the
// per-connection Handler that attaches/detaches connections to sessions.
package term

import (
	"fmt"
	"sync"
	"time"

	"github.com/famgia/remote-host/internal/launcher"
)

// maxScrollback bounds the per-session output history retained while detached
// (and replayed on attach). Older bytes are dropped once the cap is exceeded.
const maxScrollback = 256 * 1024 // 256 KiB

// Subscriber receives a session's live output and a final exit notification.
// The per-connection Handler implements it, forwarding to its WebSocket conn.
type Subscriber interface {
	Output(payload []byte)
	Exit()
}

// SessionInfo is the JSON-friendly snapshot returned by "list".
type SessionInfo struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Cols    uint16 `json:"cols"`
	Rows    uint16 `json:"rows"`
	Alive   bool   `json:"alive"`
	Created int64  `json:"created"` // unix seconds
}

// Session is a running shell owned by the Manager, independent of any one
// connection. Zero or more Subscribers may be attached at a time.
type Session struct {
	id      string
	created int64
	pty     launcher.Session
	mgr     *Manager

	mu      sync.Mutex
	title   string
	cols    uint16
	rows    uint16
	alive   bool
	ring    []byte             // capped scrollback buffer
	subs    map[int]Subscriber // attached sinks
	nextSub int
}

// ID returns the stable session identifier.
func (s *Session) ID() string { return s.id }

// Info returns a snapshot of the session's metadata.
func (s *Session) Info() SessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return SessionInfo{
		ID: s.id, Title: s.title, Cols: s.cols, Rows: s.rows,
		Alive: s.alive, Created: s.created,
	}
}

// Attach registers sub and returns a subscriber id plus a copy of the current
// scrollback so the caller can replay recent output to the newly-attached client.
func (s *Session) Attach(sub Subscriber) (subID int, scrollback []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextSub++
	id := s.nextSub
	s.subs[id] = sub
	return id, append([]byte(nil), s.ring...)
}

// Detach removes a previously-attached subscriber. The session keeps running.
func (s *Session) Detach(subID int) {
	s.mu.Lock()
	delete(s.subs, subID)
	s.mu.Unlock()
}

// Write feeds keystrokes to the PTY.
func (s *Session) Write(p []byte) error {
	_, err := s.pty.Write(p)
	return err
}

// Resize updates the PTY window size.
func (s *Session) Resize(cols, rows uint16) error {
	s.mu.Lock()
	s.cols, s.rows = cols, rows
	s.mu.Unlock()
	return s.pty.Resize(launcher.Size{Cols: cols, Rows: rows})
}

// appendRingLocked appends to the scrollback ring, trimming the oldest bytes
// past the cap. Caller holds s.mu.
func (s *Session) appendRingLocked(chunk []byte) {
	s.ring = append(s.ring, chunk...)
	if len(s.ring) > maxScrollback {
		s.ring = append([]byte(nil), s.ring[len(s.ring)-maxScrollback:]...)
	}
}

// pump reads PTY output forever, buffering it into the ring and broadcasting to
// attached subscribers. When the shell exits it notifies subscribers and the
// Manager drops the session.
func (s *Session) pump() {
	buf := make([]byte, 32*1024)
	for {
		n, err := s.pty.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			s.mu.Lock()
			s.appendRingLocked(chunk)
			subs := make([]Subscriber, 0, len(s.subs))
			for _, sub := range s.subs {
				subs = append(subs, sub)
			}
			s.mu.Unlock()
			for _, sub := range subs {
				sub.Output(chunk)
			}
		}
		if err != nil {
			break // EOF / read error — shell exited
		}
	}

	_ = s.pty.Wait()

	s.mu.Lock()
	s.alive = false
	subs := make([]Subscriber, 0, len(s.subs))
	for _, sub := range s.subs {
		subs = append(subs, sub)
	}
	s.subs = map[int]Subscriber{}
	s.mu.Unlock()

	for _, sub := range subs {
		sub.Exit()
	}
	s.mgr.remove(s.id)
}

// Manager owns every live terminal session for the host process. It is shared
// across all connections (constructed once in cmd/hostd) so sessions outlive
// the connection that created them.
type Manager struct {
	launcher launcher.Launcher

	mu       sync.Mutex
	sessions map[string]*Session
	counter  int
}

// NewManager returns a Manager that spawns shells via l.
func NewManager(l launcher.Launcher) *Manager {
	return &Manager{launcher: l, sessions: make(map[string]*Session)}
}

// Create starts a new shell session and begins pumping its output.
func (m *Manager) Create(p launcher.Profile, cols, rows uint16, title string) (*Session, error) {
	if cols == 0 {
		cols = 80
	}
	if rows == 0 {
		rows = 24
	}
	pty, err := m.launcher.StartShell(p, launcher.Size{Cols: cols, Rows: rows})
	if err != nil {
		return nil, fmt.Errorf("term: start shell: %w", err)
	}

	m.mu.Lock()
	m.counter++
	id := fmt.Sprintf("term-%d", m.counter)
	if title == "" {
		title = fmt.Sprintf("shell %d", m.counter)
	}
	s := &Session{
		id: id, created: time.Now().Unix(), pty: pty, mgr: m,
		title: title, cols: cols, rows: rows, alive: true,
		subs: make(map[int]Subscriber),
	}
	m.sessions[id] = s
	m.mu.Unlock()

	go s.pump()
	return s, nil
}

// Get returns the session with the given id, if it exists and is tracked.
func (m *Manager) Get(id string) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	return s, ok
}

// List returns a snapshot of all live sessions, newest first.
func (m *Manager) List() []SessionInfo {
	m.mu.Lock()
	sessions := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.mu.Unlock()

	out := make([]SessionInfo, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, s.Info())
	}
	// Newest first (higher counter id created later); sort by Created desc then id.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Created > out[j-1].Created; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// Kill terminates a session's shell and removes it from the Manager.
func (m *Manager) Kill(id string) error {
	m.mu.Lock()
	s, ok := m.sessions[id]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("term: no such session %q", id)
	}
	// Closing the PTY makes pump() observe EOF, which notifies subscribers and
	// calls remove(). Close is best-effort.
	return s.pty.Close()
}

// remove drops a session from the map (called by pump on exit, or after Kill).
func (m *Manager) remove(id string) {
	m.mu.Lock()
	delete(m.sessions, id)
	m.mu.Unlock()
}
