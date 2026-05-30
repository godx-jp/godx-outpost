package term

import (
	"reflect"
	"testing"
)

func TestParseTmuxSessions(t *testing.T) {
	// Lines use the \x1f field delimiter that tmuxListFormat emits.
	const out = "main\x1f3\x1f1\n" + // attached (1 client)
		"work\x1f1\x1f0\n" + // detached
		"team\x1f2\x1f2\n" + // attached by 2 clients — must still be "attached"
		"my session\x1f5\x1f0\n" // space in name survives the \x1f split

	got := parseTmuxSessions([]byte(out))
	want := []TmuxSession{
		{Name: "main", Windows: 3, Attached: true},
		{Name: "work", Windows: 1, Attached: false},
		{Name: "team", Windows: 2, Attached: true},
		{Name: "my session", Windows: 5, Attached: false},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseTmuxSessions:\n got  %+v\n want %+v", got, want)
	}
}

func TestParseTmuxSessionsEmptyAndMalformed(t *testing.T) {
	cases := map[string]string{
		"empty":          "",
		"whitespaceOnly": "  \n\n  ",
		"shortLine":      "onlyname\x1f1\n", // < 3 fields → skipped
	}
	for name, out := range cases {
		if got := parseTmuxSessions([]byte(out)); len(got) != 0 {
			t.Errorf("%s: expected no sessions, got %+v", name, got)
		}
	}
}

func TestParseTmuxSessionsBadWindowCount(t *testing.T) {
	// A non-numeric window count must not crash; it falls back to 0.
	got := parseTmuxSessions([]byte("x\x1fNaN\x1f0"))
	if len(got) != 1 || got[0].Windows != 0 || got[0].Name != "x" {
		t.Errorf("got %+v, want one session x with 0 windows", got)
	}
}

func TestParseZellijSessions(t *testing.T) {
	// Plain `-s` form: one bare name per line.
	got := parseZellijSessions([]byte("alpha\nbeta\n"))
	want := []ZellijSession{{Name: "alpha"}, {Name: "beta"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("plain form:\n got  %+v\n want %+v", got, want)
	}
}

func TestParseZellijSessionsFormatted(t *testing.T) {
	// Formatted form: ANSI colour codes + trailing metadata; only the leading
	// name is kept, EXITED rows and the "no active" notice are dropped.
	const out = "\x1b[32mfocused\x1b[0m [Created 2h ago]\n" +
		"detached [Created 5m ago]\n" +
		"old-one [Created 1d ago] (EXITED - attach to resurrect)\n" +
		"No active zellij sessions found.\n"

	got := parseZellijSessions([]byte(out))
	want := []ZellijSession{{Name: "focused"}, {Name: "detached"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("formatted form:\n got  %+v\n want %+v", got, want)
	}
}

func TestParseZellijSessionsEmpty(t *testing.T) {
	for _, out := range []string{"", "   \n  ", "No active zellij sessions found."} {
		if got := parseZellijSessions([]byte(out)); len(got) != 0 {
			t.Errorf("expected no sessions for %q, got %+v", out, got)
		}
	}
}
