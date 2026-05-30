// Package term implements the TERMINAL channel (protocol.ChTerm).
//
// Sessions are tmux-like and, when dtach is available, are backed by dtach so
// they ALSO survive a hostd restart and can be attached locally from the host
// itself ("dtach -a <socket>") with fully native terminal scrolling (dtach is
// transparent — no status bar, no copy-mode).
//
//   - dtach mode (default when `dtach` is on PATH): each session's shell runs
//     under a dtach master tied to a unix socket under <configDir>/sessions/.
//     The master is independent of hostd, so sessions outlive connection drops
//     AND hostd restarts. List() discovers sessions by scanning that directory.
//   - fallback mode (no dtach): sessions are in-process PTYs that survive
//     connection drops but die with hostd.
//
// Either way the per-connection Handler (term.go) attaches/detaches and the
// Session keeps a scrollback ring for replay-on-attach.
package term

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/famgia/remote-host/internal/launcher"
)

const maxScrollback = 256 * 1024 // 256 KiB scrollback retained per session

// Subscriber receives a session's live output and a final exit notification.
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
	Created int64  `json:"created"`
}

// sessionMeta is the per-session metadata persisted next to its dtach socket so
// sessions can be listed after a hostd restart.
type sessionMeta struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Created int64  `json:"created"`
	Cols    uint16 `json:"cols"`
	Rows    uint16 `json:"rows"`
}

// Session is a running shell. In dtach mode the real shell lives in the dtach
// master; pty here is hostd's dtach *client* whose output we mirror into ring
// and broadcast to subscribers.
type Session struct {
	id      string
	created int64
	socket  string // dtach socket path ("" in fallback mode)
	pty     launcher.Session
	mgr     *Manager

	mu      sync.Mutex
	title   string
	cols    uint16
	rows    uint16
	alive   bool
	ring    []byte
	subs    map[int]Subscriber
	nextSub int
}

func (s *Session) ID() string { return s.id }

func (s *Session) Info() SessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return SessionInfo{ID: s.id, Title: s.title, Cols: s.cols, Rows: s.rows, Alive: s.alive, Created: s.created}
}

// Attach registers sub and returns a subscriber id plus a copy of scrollback.
func (s *Session) Attach(sub Subscriber) (subID int, scrollback []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextSub++
	id := s.nextSub
	s.subs[id] = sub
	return id, append([]byte(nil), s.ring...)
}

func (s *Session) Detach(subID int) {
	s.mu.Lock()
	delete(s.subs, subID)
	s.mu.Unlock()
}

func (s *Session) Write(p []byte) error {
	_, err := s.pty.Write(p)
	return err
}

func (s *Session) Resize(cols, rows uint16) error {
	s.mu.Lock()
	s.cols, s.rows = cols, rows
	s.mu.Unlock()
	if s.socket != "" {
		s.mgr.writeMeta(s.id, sessionMeta{ID: s.id, Title: s.title, Created: s.created, Cols: cols, Rows: rows})
	}
	return s.pty.Resize(launcher.Size{Cols: cols, Rows: rows})
}

func (s *Session) appendRingLocked(chunk []byte) {
	s.ring = append(s.ring, chunk...)
	if len(s.ring) > maxScrollback {
		s.ring = append([]byte(nil), s.ring[len(s.ring)-maxScrollback:]...)
	}
}

// pump mirrors PTY output → ring + subscribers, then handles teardown.
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
			break
		}
	}
	_ = s.pty.Wait()

	// In dtach mode the client PTY can end while the master (and shell) keep
	// running. Only treat it as a real exit if the dtach socket is gone.
	exited := true
	if s.mgr.useDtach && s.socket != "" {
		exited = !socketAlive(s.socket)
	}

	s.mu.Lock()
	s.alive = false
	subs := make([]Subscriber, 0, len(s.subs))
	for _, sub := range s.subs {
		subs = append(subs, sub)
	}
	s.subs = map[int]Subscriber{}
	s.mu.Unlock()

	s.mgr.dropFromMemory(s.id)
	if exited {
		if s.socket != "" {
			_ = os.Remove(s.socket)
			_ = os.Remove(s.mgr.metaPath(s.id))
		}
		for _, sub := range subs {
			sub.Exit()
		}
	}
}

// Manager owns terminal sessions for the host process and (in dtach mode) the
// on-disk session directory shared with the host's own `dtach` clients.
type Manager struct {
	launcher launcher.Launcher
	sessDir  string
	useDtach bool

	mu       sync.Mutex
	sessions map[string]*Session
	counter  int
}

// NewManager returns a Manager. If useDtach, sessions are backed by dtach using
// sockets under sessDir (created if missing); otherwise sessions are in-process.
func NewManager(l launcher.Launcher, sessDir string, useDtach bool) *Manager {
	if useDtach && sessDir != "" {
		_ = os.MkdirAll(sessDir, 0o700)
	}
	return &Manager{launcher: l, sessDir: sessDir, useDtach: useDtach, sessions: make(map[string]*Session)}
}

func (m *Manager) sockPath(id string) string { return filepath.Join(m.sessDir, id+".sock") }
func (m *Manager) metaPath(id string) string { return filepath.Join(m.sessDir, id+".json") }

func (m *Manager) writeMeta(id string, meta sessionMeta) {
	data, err := json.Marshal(meta)
	if err == nil {
		_ = os.WriteFile(m.metaPath(id), data, 0o600)
	}
}

func (m *Manager) readMeta(id string) sessionMeta {
	var meta sessionMeta
	if data, err := os.ReadFile(m.metaPath(id)); err == nil {
		_ = json.Unmarshal(data, &meta)
	}
	if meta.ID == "" {
		meta.ID = id
	}
	return meta
}

func (m *Manager) register(s *Session) {
	m.mu.Lock()
	m.sessions[s.id] = s
	m.mu.Unlock()
}

func (m *Manager) dropFromMemory(id string) {
	m.mu.Lock()
	delete(m.sessions, id)
	m.mu.Unlock()
}

// Create starts a new shell session.
func (m *Manager) Create(p launcher.Profile, cols, rows uint16, title string) (*Session, error) {
	if cols == 0 {
		cols = 80
	}
	if rows == 0 {
		rows = 24
	}
	m.mu.Lock()
	m.counter++
	n := m.counter
	m.mu.Unlock()
	id := randID()
	if title == "" {
		title = fmt.Sprintf("shell %d", n)
	}

	var pty launcher.Session
	var err error
	var sock string
	if m.useDtach {
		sock = m.sockPath(id)
		m.writeMeta(id, sessionMeta{ID: id, Title: title, Created: time.Now().Unix(), Cols: cols, Rows: rows})
		// dtach -A: attach, creating the session (running the shell) if needed.
		// -z: no suspend key; -r winch: redraw via SIGWINCH on (re)attach.
		pty, err = m.launcher.StartCommand(p, launcher.Size{Cols: cols, Rows: rows},
			"dtach", "-A", sock, "-z", "-r", "winch", shellFor(p))
	} else {
		pty, err = m.launcher.StartShell(p, launcher.Size{Cols: cols, Rows: rows})
	}
	if err != nil {
		if sock != "" {
			_ = os.Remove(sock)
			_ = os.Remove(m.metaPath(id))
		}
		return nil, fmt.Errorf("term: start session: %w", err)
	}

	s := m.newSession(id, title, time.Now().Unix(), pty, cols, rows, sock)
	m.register(s)
	go s.pump()
	return s, nil
}

// Attach returns the session by id, reviving it from disk (re-attaching a dtach
// client) if it is not in memory — e.g. after a hostd restart.
func (m *Manager) Attach(p launcher.Profile, id string) (*Session, error) {
	if s, ok := m.Get(id); ok {
		return s, nil
	}
	if !m.useDtach {
		return nil, fmt.Errorf("term: no such session %q", id)
	}
	sock := m.sockPath(id)
	if !socketAlive(sock) {
		return nil, fmt.Errorf("term: session %q is not running", id)
	}
	meta := m.readMeta(id)
	cols, rows := meta.Cols, meta.Rows
	if cols == 0 {
		cols = 80
	}
	if rows == 0 {
		rows = 24
	}
	// dtach -a takes the socket immediately after the flag, then options.
	pty, err := m.launcher.StartCommand(p, launcher.Size{Cols: cols, Rows: rows},
		"dtach", "-a", sock, "-r", "winch")
	if err != nil {
		return nil, fmt.Errorf("term: re-attach: %w", err)
	}
	s := m.newSession(id, meta.Title, meta.Created, pty, cols, rows, sock)
	m.register(s)
	go s.pump()
	return s, nil
}

func (m *Manager) newSession(id, title string, created int64, pty launcher.Session, cols, rows uint16, sock string) *Session {
	return &Session{
		id: id, title: title, created: created, socket: sock, pty: pty, mgr: m,
		cols: cols, rows: rows, alive: true, subs: make(map[int]Subscriber),
	}
}

// Get returns an in-memory session.
func (m *Manager) Get(id string) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	return s, ok
}

// List returns all sessions: in-memory ones plus (in dtach mode) any sessions
// discovered on disk — including ones started before a hostd restart.
func (m *Manager) List() []SessionInfo {
	byID := make(map[string]SessionInfo)

	if m.useDtach && m.sessDir != "" {
		entries, _ := os.ReadDir(m.sessDir)
		for _, e := range entries {
			name := e.Name()
			if filepath.Ext(name) != ".json" {
				continue
			}
			id := name[:len(name)-len(".json")]
			meta := m.readMeta(id)
			alive := socketAlive(m.sockPath(id))
			if !alive {
				// Stale session — its master is gone; clean it up.
				_ = os.Remove(m.sockPath(id))
				_ = os.Remove(m.metaPath(id))
				continue
			}
			byID[id] = SessionInfo{ID: id, Title: meta.Title, Cols: meta.Cols, Rows: meta.Rows, Alive: true, Created: meta.Created}
		}
	}

	m.mu.Lock()
	for _, s := range m.sessions {
		byID[s.id] = s.Info()
	}
	m.mu.Unlock()

	out := make([]SessionInfo, 0, len(byID))
	for _, info := range byID {
		out = append(out, info)
	}
	// Newest first.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Created > out[j-1].Created; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// Kill terminates a session (its shell + dtach master) and removes it.
func (m *Manager) Kill(id string) error {
	m.mu.Lock()
	s, ok := m.sessions[id]
	m.mu.Unlock()

	if m.useDtach {
		sock := m.sockPath(id)
		// Terminate the dtach master (and any clients) for this socket. Only
		// dtach processes carry the socket path in their argv, so this is safe.
		_ = exec.Command("pkill", "-f", sock).Run()
		_ = os.Remove(sock)
		_ = os.Remove(m.metaPath(id))
	}
	if ok {
		_ = s.pty.Close()
		m.dropFromMemory(id)
		return nil
	}
	if !m.useDtach {
		return fmt.Errorf("term: no such session %q", id)
	}
	return nil
}

// ---- helpers ----------------------------------------------------------------

// socketAlive reports whether a dtach master is listening on sock.
func socketAlive(sock string) bool {
	if _, err := os.Stat(sock); err != nil {
		return false
	}
	conn, err := net.DialTimeout("unix", sock, 400*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// shellFor resolves the shell for a profile (mirrors the direct launcher).
func shellFor(p launcher.Profile) string {
	if p.Shell != "" {
		return p.Shell
	}
	if s := os.Getenv("SHELL"); s != "" {
		return s
	}
	return "/bin/sh"
}

// randID returns a short random session id stable across restarts (no counter
// reuse collisions when sessions persist on disk).
func randID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("t-%d", time.Now().UnixNano())
	}
	return "t-" + hex.EncodeToString(b)
}
