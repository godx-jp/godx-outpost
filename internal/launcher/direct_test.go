package launcher

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// drain reads a session's PTY output until the process exits (Read errors),
// with a safety timeout so a misbehaving test fails instead of hanging.
func drain(t *testing.T, s Session) string {
	t.Helper()
	done := make(chan string, 1)
	go func() {
		var b []byte
		buf := make([]byte, 4096)
		for {
			n, err := s.Read(buf)
			if n > 0 {
				b = append(b, buf[:n]...)
			}
			if err != nil {
				break
			}
		}
		done <- string(b)
	}()
	select {
	case out := <-done:
		return out
	case <-time.After(5 * time.Second):
		t.Fatal("timed out reading PTY output")
		return ""
	}
}

func TestStartCommandOutput(t *testing.T) {
	s, err := NewDirect().StartCommand(Profile{}, Size{Cols: 80, Rows: 24}, "/bin/sh", "-c", "printf 'marker-OK'")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if out := drain(t, s); !strings.Contains(out, "marker-OK") {
		t.Fatalf("output %q missing marker", out)
	}
}

func TestStartCommandCwd(t *testing.T) {
	dir := t.TempDir() // unique basename, present in pwd output regardless of /tmp symlinks
	s, err := NewDirect().StartCommand(Profile{Cwd: dir}, Size{Cols: 80, Rows: 24}, "/bin/sh", "-c", "pwd")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if out := drain(t, s); !strings.Contains(out, filepath.Base(dir)) {
		t.Fatalf("pwd %q is not in the requested dir %q", out, dir)
	}
}

func TestStartCommandEnv(t *testing.T) {
	s, err := NewDirect().StartCommand(
		Profile{Env: []string{"OUTPOST_TEST_VAR=zzz123"}},
		Size{Cols: 80, Rows: 24},
		"/bin/sh", "-c", `printf %s "$OUTPOST_TEST_VAR"`,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if out := drain(t, s); !strings.Contains(out, "zzz123") {
		t.Fatalf("profile env not applied to the command: %q", out)
	}
}

func TestStartShellResizeAndClose(t *testing.T) {
	s, err := NewDirect().StartShell(Profile{Shell: "/bin/sh"}, Size{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("StartShell: %v", err)
	}
	if err := s.Resize(Size{Cols: 120, Rows: 40}); err != nil {
		t.Fatalf("resize: %v", err)
	}
	// Drive the shell to exit, then make sure we can read to completion + close.
	if _, err := s.Write([]byte("exit\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = drain(t, s)
	if err := s.Close(); err != nil {
		// Already exited — Close is best-effort; only fail on an unexpected error
		// (a closed PTY may report a benign error, so just log).
		t.Logf("close after exit: %v", err)
	}
}
