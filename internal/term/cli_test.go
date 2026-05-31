package term

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/godx-jp/godx-outpost/internal/store"
)

func TestExpandUserDir(t *testing.T) {
	home, _ := os.UserHomeDir()
	if got := expandUserDir(""); got != "" {
		t.Fatalf(`expandUserDir("") = %q, want ""`, got)
	}
	if got := expandUserDir("/no/such/dir/xyz123"); got != "" {
		t.Fatalf("nonexistent dir → %q, want \"\"", got)
	}
	if home != "" {
		if got := expandUserDir("~"); got != home {
			t.Fatalf("expandUserDir(~) = %q, want %q", got, home)
		}
	}
	d := t.TempDir()
	if got := expandUserDir(d); got != d {
		t.Fatalf("existing dir → %q, want %q", got, d)
	}
	// A regular file is not a directory → rejected.
	f := filepath.Join(d, "afile")
	_ = os.WriteFile(f, []byte("x"), 0o600)
	if got := expandUserDir(f); got != "" {
		t.Fatalf("file path → %q, want \"\"", got)
	}
}

func TestNewAndListLocalSession(t *testing.T) {
	sessDir := t.TempDir()
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	id, sock, shell, _, err := NewLocalSession(sessDir, "", "/bin/zsh", "work", st)
	if err != nil {
		t.Fatal(err)
	}
	if id == "" || sock == "" || shell != "/bin/zsh" {
		t.Fatalf("bad return id=%q sock=%q shell=%q", id, sock, shell)
	}
	if _, err := os.Stat(filepath.Join(sessDir, id+".json")); err != nil {
		t.Fatalf("socket-meta not written: %v", err)
	}

	rows, _ := st.ListSessions()
	found := false
	for _, r := range rows {
		if r.ID == id {
			found = true
			if r.Title != "work" {
				t.Fatalf("store title = %q, want work", r.Title)
			}
		}
	}
	if !found {
		t.Fatal("session not recorded in the store")
	}

	// LocalSessions lists it. Alive is false: NewLocalSession does the
	// bookkeeping but doesn't start the dtach master (the caller execs it).
	var got *LocalSession
	for _, s := range LocalSessions(sessDir, st) {
		if s.ID == id {
			s := s
			got = &s
		}
	}
	if got == nil {
		t.Fatal("LocalSessions did not return the new session")
	}
	if got.Title != "work" {
		t.Fatalf("listed title = %q, want work", got.Title)
	}
	if got.Alive {
		t.Fatal("Alive should be false with no master running")
	}
	if _, alive := SessionSocketAlive(sessDir, id); alive {
		t.Fatal("SessionSocketAlive should be false for a session with no master")
	}
	if _, alive := SessionSocketAlive(sessDir, "does-not-exist"); alive {
		t.Fatal("SessionSocketAlive should be false for an unknown id")
	}
}
