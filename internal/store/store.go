// Package store is the hostd persistent store (SQLite via modernc — pure Go, no
// cgo, so hostd stays a single self-contained binary).
//
// It records two things that must outlive a daemon restart or a reboot:
//
//   - devices: every paired client (so logins can be listed and revoked
//     individually, instead of the all-or-nothing generation bump).
//   - sessions: terminal session metadata incl. working directory, so after a
//     reboot `hostd restore` can re-open each session in the same folder
//     (a fresh shell — the original processes are gone with the reboot).
package store

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// Store wraps the SQLite database.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the database at path and applies the schema.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	db.SetMaxOpenConns(1) // simplest correct concurrency for a small local DB
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	const schema = `
CREATE TABLE IF NOT EXISTS devices (
  client_id TEXT PRIMARY KEY,
  name      TEXT NOT NULL DEFAULT '',
  type      TEXT NOT NULL DEFAULT '',
  paired_at INTEGER NOT NULL,
  last_seen INTEGER NOT NULL,
  revoked   INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS sessions (
  id        TEXT PRIMARY KEY,
  title     TEXT NOT NULL DEFAULT '',
  cwd       TEXT NOT NULL DEFAULT '',
  shell     TEXT NOT NULL DEFAULT '',
  cols      INTEGER NOT NULL DEFAULT 80,
  rows      INTEGER NOT NULL DEFAULT 24,
  created   INTEGER NOT NULL,
  last_seen INTEGER NOT NULL,
  archived  INTEGER NOT NULL DEFAULT 0
);`
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("store: migrate: %w", err)
	}
	// Add columns introduced after the first release (ignore "duplicate column").
	_, _ = s.db.Exec(`ALTER TABLE devices ADD COLUMN type TEXT NOT NULL DEFAULT ''`)
	return nil
}

// ---- devices ----------------------------------------------------------------

// Device is a paired client.
type Device struct {
	ClientID string `json:"clientId"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	PairedAt int64  `json:"pairedAt"`
	LastSeen int64  `json:"lastSeen"`
	Revoked  bool   `json:"revoked"`
}

// AddDevice records a freshly paired client (name/type supplied by the client).
func (s *Store) AddDevice(clientID, name, devType string) error {
	now := time.Now().Unix()
	_, err := s.db.Exec(
		`INSERT INTO devices(client_id,name,type,paired_at,last_seen,revoked) VALUES(?,?,?,?,?,0)
		 ON CONFLICT(client_id) DO UPDATE SET name=excluded.name, type=excluded.type, last_seen=excluded.last_seen, revoked=0`,
		clientID, name, devType, now, now)
	return err
}

// RenameDevice updates a device's display name.
func (s *Store) RenameDevice(clientID, name string) error {
	res, err := s.db.Exec(`UPDATE devices SET name=? WHERE client_id=?`, name, clientID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("store: no such device %q", clientID)
	}
	return nil
}

// DeviceActive reports whether a client exists and is not revoked.
func (s *Store) DeviceActive(clientID string) (bool, error) {
	var revoked int
	err := s.db.QueryRow(`SELECT revoked FROM devices WHERE client_id=?`, clientID).Scan(&revoked)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return revoked == 0, nil
}

// TouchDevice updates a device's last-seen timestamp (best-effort).
func (s *Store) TouchDevice(clientID string) {
	_, _ = s.db.Exec(`UPDATE devices SET last_seen=? WHERE client_id=?`, time.Now().Unix(), clientID)
}

// RevokeDevice marks one device revoked; its tokens stop working.
func (s *Store) RevokeDevice(clientID string) error {
	res, err := s.db.Exec(`UPDATE devices SET revoked=1 WHERE client_id=?`, clientID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("store: no such device %q", clientID)
	}
	return nil
}

// ListDevices returns all known devices, newest pairing first.
func (s *Store) ListDevices() ([]Device, error) {
	rows, err := s.db.Query(`SELECT client_id,name,type,paired_at,last_seen,revoked FROM devices ORDER BY paired_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Device
	for rows.Next() {
		var d Device
		var rev int
		if err := rows.Scan(&d.ClientID, &d.Name, &d.Type, &d.PairedAt, &d.LastSeen, &rev); err != nil {
			return nil, err
		}
		d.Revoked = rev != 0
		out = append(out, d)
	}
	return out, rows.Err()
}

// ---- sessions ---------------------------------------------------------------

// SessionRow is persisted terminal-session metadata.
type SessionRow struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Cwd      string `json:"cwd"`
	Shell    string `json:"shell"`
	Cols     int    `json:"cols"`
	Rows     int    `json:"rows"`
	Created  int64  `json:"created"`
	LastSeen int64  `json:"lastSeen"`
	Archived bool   `json:"archived"`
}

// UpsertSession inserts or updates a session's metadata.
func (s *Store) UpsertSession(r SessionRow) error {
	_, err := s.db.Exec(
		`INSERT INTO sessions(id,title,cwd,shell,cols,rows,created,last_seen,archived)
		 VALUES(?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(id) DO UPDATE SET
		   title=excluded.title, cwd=excluded.cwd, shell=excluded.shell,
		   cols=excluded.cols, rows=excluded.rows, last_seen=excluded.last_seen,
		   archived=excluded.archived`,
		r.ID, r.Title, r.Cwd, r.Shell, r.Cols, r.Rows, r.Created, r.LastSeen, b2i(r.Archived))
	return err
}

// SetSessionCwd records a session's latest working directory (best-effort).
func (s *Store) SetSessionCwd(id, cwd string) {
	if cwd == "" {
		return
	}
	_, _ = s.db.Exec(`UPDATE sessions SET cwd=?, last_seen=? WHERE id=?`, cwd, time.Now().Unix(), id)
}

// SetArchived flips a session's archived flag (true when its master is gone).
func (s *Store) SetArchived(id string, archived bool) {
	_, _ = s.db.Exec(`UPDATE sessions SET archived=? WHERE id=?`, b2i(archived), id)
}

// DeleteSession removes a session row.
func (s *Store) DeleteSession(id string) {
	_, _ = s.db.Exec(`DELETE FROM sessions WHERE id=?`, id)
}

// ListSessions returns all recorded sessions, newest first.
func (s *Store) ListSessions() ([]SessionRow, error) {
	rows, err := s.db.Query(`SELECT id,title,cwd,shell,cols,rows,created,last_seen,archived FROM sessions ORDER BY created DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionRow
	for rows.Next() {
		var r SessionRow
		var arch int
		if err := rows.Scan(&r.ID, &r.Title, &r.Cwd, &r.Shell, &r.Cols, &r.Rows, &r.Created, &r.LastSeen, &arch); err != nil {
			return nil, err
		}
		r.Archived = arch != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
