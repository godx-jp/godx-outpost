package store

import (
	"path/filepath"
	"testing"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestDeviceLifecycle(t *testing.T) {
	s := openTest(t)

	// Unknown device → inactive, not found.
	if active, err := s.DeviceActive("ghost"); err != nil || active {
		t.Fatalf("unknown device: active=%v err=%v, want false,nil", active, err)
	}

	if err := s.AddDevice("cid1", "Phone", "iPhone"); err != nil {
		t.Fatal(err)
	}
	if active, err := s.DeviceActive("cid1"); err != nil || !active {
		t.Fatalf("after add: active=%v err=%v, want true", active, err)
	}

	devs, err := s.ListDevices()
	if err != nil {
		t.Fatal(err)
	}
	if len(devs) != 1 || devs[0].ClientID != "cid1" || devs[0].Name != "Phone" || devs[0].Type != "iPhone" || devs[0].Revoked {
		t.Fatalf("ListDevices = %+v", devs)
	}

	// Rename.
	if err := s.RenameDevice("cid1", "iPad"); err != nil {
		t.Fatal(err)
	}
	if devs, _ := s.ListDevices(); devs[0].Name != "iPad" {
		t.Fatalf("rename not applied: %q", devs[0].Name)
	}

	// Revoke → inactive + flagged.
	if err := s.RevokeDevice("cid1"); err != nil {
		t.Fatal(err)
	}
	if active, _ := s.DeviceActive("cid1"); active {
		t.Fatal("device should be inactive after revoke")
	}
	if devs, _ := s.ListDevices(); !devs[0].Revoked {
		t.Fatal("device should be flagged revoked")
	}

	// Re-pairing (AddDevice with same id) un-revokes via ON CONFLICT.
	if err := s.AddDevice("cid1", "iPad", "iPad"); err != nil {
		t.Fatal(err)
	}
	if active, _ := s.DeviceActive("cid1"); !active {
		t.Fatal("re-pairing should re-activate the device")
	}
	if devs, _ := s.ListDevices(); len(devs) != 1 {
		t.Fatalf("re-pair should update in place, got %d devices", len(devs))
	}

	// Operations on unknown devices error.
	if err := s.RenameDevice("nope", "x"); err == nil {
		t.Fatal("rename unknown device should error")
	}
	if err := s.RevokeDevice("nope"); err == nil {
		t.Fatal("revoke unknown device should error")
	}
}

func TestSessionLifecycle(t *testing.T) {
	s := openTest(t)

	row := SessionRow{ID: "t-1", Title: "work", Cwd: "/a", Shell: "/bin/zsh", Cols: 100, Rows: 30, Created: 111, LastSeen: 111}
	if err := s.UpsertSession(row); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != "t-1" || list[0].Title != "work" || list[0].Cols != 100 {
		t.Fatalf("ListSessions = %+v", list)
	}

	// Upsert same id updates in place (no duplicate).
	row.Title = "renamed"
	if err := s.UpsertSession(row); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.ListSessions(); len(list) != 1 || list[0].Title != "renamed" {
		t.Fatalf("upsert should update in place: %+v", list)
	}

	// cwd + archived mutators.
	s.SetSessionCwd("t-1", "/new/dir")
	s.SetArchived("t-1", true)
	if list, _ := s.ListSessions(); list[0].Cwd != "/new/dir" || !list[0].Archived {
		t.Fatalf("cwd/archived not applied: %+v", list[0])
	}

	// Delete.
	s.DeleteSession("t-1")
	if list, _ := s.ListSessions(); len(list) != 0 {
		t.Fatalf("delete failed, still %d rows", len(list))
	}
}

func TestPersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.db")
	s1, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = s1.AddDevice("cid", "n", "t")
	_ = s1.UpsertSession(SessionRow{ID: "s", Created: 1, LastSeen: 1})
	_ = s1.Close()

	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if active, _ := s2.DeviceActive("cid"); !active {
		t.Fatal("device did not persist across reopen")
	}
	if list, _ := s2.ListSessions(); len(list) != 1 {
		t.Fatal("session did not persist across reopen")
	}
}
