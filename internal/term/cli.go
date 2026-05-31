package term

// Host-side helpers for the `outpost` CLI (ls / attach / new). These operate
// DIRECTLY on the dtach sockets + the store on disk — no daemon, no WebSocket.
// Because sessions are just dtach sockets under sessDir, a session created from
// the host CLI and one created by the mobile app (over the daemon) are the same
// thing: both are listed by the daemon's Manager.List (which scans sessDir) and
// both can be attached locally with `dtach -a`. This is the tmux-like sharing.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/godx-jp/godx-outpost/internal/launcher"
	"github.com/godx-jp/godx-outpost/internal/store"
)

// LocalSession is a host-side view of a dtach-backed session for `outpost ls`.
type LocalSession struct {
	ID    string
	Title string
	Cwd   string
	Alive bool
}

// LocalSessions lists the dtach-backed sessions under sessDir, cross-referenced
// with the store for title/cwd, marking which are still running.
func LocalSessions(sessDir string, st *store.Store) []LocalSession {
	rows := map[string]store.SessionRow{}
	if st != nil {
		if list, err := st.ListSessions(); err == nil {
			for _, r := range list {
				rows[r.ID] = r
			}
		}
	}
	entries, _ := os.ReadDir(sessDir)
	var out []LocalSession
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".json" {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		var m sessionMeta
		if data, err := os.ReadFile(filepath.Join(sessDir, e.Name())); err == nil {
			_ = json.Unmarshal(data, &m)
		}
		title, cwd := m.Title, ""
		if r, ok := rows[id]; ok {
			if title == "" {
				title = r.Title
			}
			cwd = r.Cwd
		}
		out = append(out, LocalSession{
			ID: id, Title: title, Cwd: cwd,
			Alive: socketAlive(filepath.Join(sessDir, id+".sock")),
		})
	}
	return out
}

// SessionSocketAlive returns a session's dtach socket path and whether it is
// currently running (its master is reachable).
func SessionSocketAlive(sessDir, id string) (string, bool) {
	sock := filepath.Join(sessDir, id+".sock")
	return sock, socketAlive(sock)
}

// NewLocalSession registers a fresh dtach-backed session on disk (socket-meta +
// store row) so it is identical to one the daemon creates and discoverable by
// the app and `outpost ls`. It does NOT start the master; the caller execs
// `dtach -A <sock> -z -r winch <shell>` (create + attach) in the foreground.
// Returns the new id, socket path, the shell to run, and the env to exec with.
func NewLocalSession(sessDir, shellrcDir, shell, title string, st *store.Store) (id, sock, sh string, env []string, err error) {
	sh = shell
	if sh == "" {
		sh = shellFor(launcher.Profile{})
	}
	if err = os.MkdirAll(sessDir, 0o700); err != nil {
		return
	}
	id = randID()
	if strings.TrimSpace(title) == "" {
		title = "shell"
	}
	sock = filepath.Join(sessDir, id+".sock")
	cols, rows := uint16(80), uint16(24)
	created := time.Now().Unix()

	meta := sessionMeta{ID: id, Title: title, Created: created, Cols: cols, Rows: rows}
	if data, merr := json.Marshal(meta); merr == nil {
		_ = os.WriteFile(filepath.Join(sessDir, id+".json"), data, 0o600)
	}
	cwd, _ := os.Getwd() // start where the user ran the CLI (tmux-like)
	if st != nil {
		_ = st.UpsertSession(store.SessionRow{
			ID: id, Title: title, Cwd: cwd, Shell: sh,
			Cols: int(cols), Rows: int(rows), Created: created, LastSeen: created,
		})
	}
	env = os.Environ()
	if shellrcDir != "" && strings.Contains(filepath.Base(sh), "zsh") {
		env = append(env, "ZDOTDIR="+shellrcDir)
	}
	return
}
