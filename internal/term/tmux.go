package term

import (
	"os/exec"
	"strconv"
	"strings"
)

// TmuxSession is one session reported by the host's tmux server.
type TmuxSession struct {
	Name     string `json:"name"`
	Windows  int    `json:"windows"`
	Attached bool   `json:"attached"`
}

// TmuxAvailable reports whether the `tmux` binary is on PATH.
func TmuxAvailable() bool {
	_, err := exec.LookPath("tmux")
	return err == nil
}

// tmuxListFormat is the -F format string for list-sessions. The unit-separator
// (\x1f) field delimiter survives spaces and special chars in session names.
const tmuxListFormat = "#{session_name}\x1f#{session_windows}\x1f#{session_attached}"

// ListTmux returns the host's current tmux sessions (empty if tmux isn't
// installed or no server is running). It never errors — a missing server is a
// normal, empty result.
func ListTmux() []TmuxSession {
	if !TmuxAvailable() {
		return nil
	}
	out, err := exec.Command("tmux", "list-sessions", "-F", tmuxListFormat).Output()
	if err != nil {
		return nil // no server running, or no sessions
	}
	return parseTmuxSessions(out)
}

// parseTmuxSessions parses the output of `tmux list-sessions -F tmuxListFormat`.
// It is separated from the exec call so parsing can be unit-tested without a
// running tmux server.
func parseTmuxSessions(out []byte) []TmuxSession {
	var res []TmuxSession
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		f := strings.Split(line, "\x1f")
		if len(f) < 3 {
			continue
		}
		windows, _ := strconv.Atoi(f[1])
		// session_attached is the NUMBER of attached clients, not a 0/1 flag, so
		// any non-zero count means the session is attached.
		attached := f[2] != "" && f[2] != "0"
		res = append(res, TmuxSession{Name: f[0], Windows: windows, Attached: attached})
	}
	return res
}

// KillTmuxSession terminates a tmux session by name.
func KillTmuxSession(name string) error {
	if !TmuxAvailable() {
		return nil
	}
	return exec.Command("tmux", "kill-session", "-t", name).Run()
}
