package launcher

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/creack/pty"
)

// directLauncher starts shells directly on the host in a PTY, with no
// sandboxing. It is suitable for admin profiles (M1–M3).
type directLauncher struct{}

// NewDirect returns a Launcher that spawns shells directly on the host.
func NewDirect() Launcher {
	return &directLauncher{}
}

// StartShell launches p.Shell (falling back to $SHELL then /bin/sh) in a PTY
// with the given initial window size. Sandbox fields in p are ignored.
func (d *directLauncher) StartShell(p Profile, size Size) (Session, error) {
	return d.StartCommand(p, size, resolveShell(p))
}

// resolveShell picks the shell for a profile: p.Shell, else $SHELL, else /bin/sh.
func resolveShell(p Profile) string {
	if p.Shell != "" {
		return p.Shell
	}
	if s := os.Getenv("SHELL"); s != "" {
		return s
	}
	return "/bin/sh"
}

// StartCommand launches name+args in a PTY with the profile's cwd/env.
func (d *directLauncher) StartCommand(p Profile, size Size, name string, args ...string) (Session, error) {
	cwd := p.Cwd
	if cwd == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("launcher/direct: resolve home dir: %w", err)
		}
		cwd = home
	}

	cmd := exec.Command(name, args...)
	cmd.Dir = cwd
	// p.Env is appended last so a profile can intentionally override host env.
	// SECURITY INVARIANT: p.Env must only ever be populated from server-side
	// profile configuration, NEVER from client/wire input — otherwise a client
	// could shadow PATH/LD_PRELOAD and hijack the shell. The wire protocol does
	// not expose Env today; keep it that way (M5 guest profiles must sanitize).
	cmd.Env = append(os.Environ(), p.Env...)

	ws := &pty.Winsize{
		Cols: size.Cols,
		Rows: size.Rows,
	}

	ptmx, err := pty.StartWithSize(cmd, ws)
	if err != nil {
		return nil, fmt.Errorf("launcher/direct: start pty: %w", err)
	}

	return &directSession{cmd: cmd, ptmx: ptmx}, nil
}

// directSession implements Session by proxying I/O through a PTY file.
type directSession struct {
	cmd  *exec.Cmd
	ptmx *os.File
}

// Read returns output from the shell's PTY (combined stdout + stderr).
func (s *directSession) Read(p []byte) (int, error) {
	return s.ptmx.Read(p)
}

// Write sends bytes to the shell's PTY (stdin / keystrokes).
func (s *directSession) Write(p []byte) (int, error) {
	return s.ptmx.Write(p)
}

// Resize updates the PTY window size.
func (s *directSession) Resize(size Size) error {
	ws := &pty.Winsize{
		Cols: size.Cols,
		Rows: size.Rows,
	}
	return pty.Setsize(s.ptmx, ws)
}

// Wait blocks until the shell process exits and returns its exit error (nil on
// a clean exit). It is safe to call concurrently with Read/Write.
func (s *directSession) Wait() error {
	return s.cmd.Wait()
}

// Close kills the shell process and closes the PTY file descriptor.
func (s *directSession) Close() error {
	// Best-effort kill; ignore the error if the process is already gone.
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	return s.ptmx.Close()
}
