// Package launcher abstracts how a remote session's shell process is spawned.
//
// The term channel calls Launcher.StartShell instead of spawning $SHELL
// directly, so the isolation mechanism is a pluggable detail:
//
//   - DirectLauncher — PTY straight on the host (admin profile). Used by M1–M3.
//   - bwrap/namespaces (Linux), sandbox-exec (macOS), container — added at M5.
//
// A session's Profile (resolved from its auth token) selects the launcher and
// constrains what the shell may see. See docs/PLAN.md "Session profiles &
// sandbox".
package launcher

// Profile describes the privileges and isolation of a single session.
//
// M1–M3 only use Admin + Shell/Env/Cwd. The sandbox fields (Root, Net*, limits)
// are honored by the Linux/macOS sandbox launchers added at M5; the direct
// launcher ignores them.
type Profile struct {
	Name  string // human label, e.g. "admin", "guest"
	Admin bool   // true → DirectLauncher (unsandboxed host access)

	Shell string   // shell to run; "" → $SHELL (fallback /bin/sh)
	Cwd   string   // working directory; "" → user home
	Env   []string // extra KEY=VALUE entries appended to the environment

	// --- sandbox knobs (M5; ignored by DirectLauncher) ---
	Root        string // filesystem root the session may see ("" → host root)
	IsolateNet  bool   // run inside a network namespace / sandbox net policy
	CPUMaxPct   int    // cgroup CPU cap, 0 = unlimited
	MemMaxBytes int64  // cgroup memory cap, 0 = unlimited
}

// Size is a terminal window size in character cells.
type Size struct {
	Cols uint16
	Rows uint16
}

// Session is a running shell attached to a PTY.
//
// Read returns the shell's combined stdout/stderr; Write feeds keystrokes to
// its stdin. Both are byte streams (the term channel ferries them over binary
// WebSocket frames). Closing the session terminates the shell.
type Session interface {
	Read(p []byte) (int, error)
	Write(p []byte) (int, error)
	Resize(s Size) error
	// Wait blocks until the shell exits and returns its exit error (nil on
	// clean exit). It is safe to call concurrently with Read/Write.
	Wait() error
	Close() error
}

// Launcher spawns shell sessions according to a Profile.
type Launcher interface {
	// StartShell launches a shell with the given profile and initial PTY size.
	StartShell(p Profile, size Size) (Session, error)
}
