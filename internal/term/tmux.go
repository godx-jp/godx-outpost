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

// ListTmux returns the host's current tmux sessions (empty if tmux isn't
// installed or no server is running). It never errors — a missing server is a
// normal, empty result.
func ListTmux() []TmuxSession {
	if !TmuxAvailable() {
		return nil
	}
	// Unit-separator (\x1f) field delimiter survives spaces/special chars in names.
	out, err := exec.Command("tmux", "list-sessions", "-F",
		"#{session_name}\x1f#{session_windows}\x1f#{session_attached}").Output()
	if err != nil {
		return nil // no server running, or no sessions
	}
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
		res = append(res, TmuxSession{Name: f[0], Windows: windows, Attached: f[2] == "1"})
	}
	return res
}
