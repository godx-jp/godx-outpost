package term

import (
	"os/exec"
	"regexp"
	"strings"
)

// ZellijSession is one session reported by the host's zellij.
type ZellijSession struct {
	Name string `json:"name"`
}

var ansiRe = regexp.MustCompile("\x1b\\[[0-9;]*[A-Za-z]")

// ZellijAvailable reports whether the `zellij` binary is on PATH.
func ZellijAvailable() bool {
	_, err := exec.LookPath("zellij")
	return err == nil
}

// ListZellij returns the host's running zellij sessions (empty if zellij isn't
// installed or no server is running). Exited sessions are skipped.
func ListZellij() []ZellijSession {
	if !ZellijAvailable() {
		return nil
	}
	out, err := exec.Command("zellij", "list-sessions", "-s").Output()
	if err != nil {
		// Older zellij has no -s; fall back to the formatted form.
		out, err = exec.Command("zellij", "list-sessions").Output()
		if err != nil {
			return nil // no server, or no sessions
		}
	}
	return parseZellijSessions(out)
}

// parseZellijSessions parses the output of `zellij list-sessions`, tolerating
// both the plain (`-s`) and the ANSI-coloured formatted forms. Exited sessions
// and the "no active sessions" notice are skipped. Separated from the exec call
// so parsing can be unit-tested without a running zellij server.
func parseZellijSessions(out []byte) []ZellijSession {
	var res []ZellijSession
	for _, raw := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line := strings.TrimSpace(ansiRe.ReplaceAllString(raw, ""))
		if line == "" || strings.Contains(line, "No active") || strings.Contains(line, "EXITED") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		res = append(res, ZellijSession{Name: fields[0]})
	}
	return res
}

// KillZellijSession stops a running zellij session and removes any exited remnant.
func KillZellijSession(name string) error {
	if !ZellijAvailable() {
		return nil
	}
	_ = exec.Command("zellij", "kill-session", name).Run()
	_ = exec.Command("zellij", "delete-session", name).Run()
	return nil
}
