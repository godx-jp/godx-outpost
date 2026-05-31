package auth

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newMgr(t *testing.T) *Manager {
	t.Helper()
	m, err := LoadFrom(t.TempDir())
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}

func TestIdentityPersistsAndPerms(t *testing.T) {
	dir := t.TempDir()
	m1, err := LoadFrom(dir)
	if err != nil {
		t.Fatal(err)
	}
	id1 := m1.DeviceID()
	_ = m1.Close()

	m2, err := LoadFrom(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer m2.Close()
	if m2.DeviceID() != id1 {
		t.Fatalf("device id changed across reload: %s → %s", id1, m2.DeviceID())
	}
	fi, err := os.Stat(filepath.Join(dir, identityFile))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("identity.json perms = %o, want 600", fi.Mode().Perm())
	}
}

func TestIdentityPermTightenedOnLoad(t *testing.T) {
	dir := t.TempDir()
	m, _ := LoadFrom(dir)
	_ = m.Close()
	p := filepath.Join(dir, identityFile)
	if err := os.Chmod(p, 0o644); err != nil { // loosen as if restored from backup
		t.Fatal(err)
	}
	m2, err := LoadFrom(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer m2.Close()
	fi, _ := os.Stat(p)
	if fi.Mode().Perm()&0o077 != 0 {
		t.Fatalf("signing-key perms not tightened on load: %o", fi.Mode().Perm())
	}
}

func TestPairingSingleUseAndAuth(t *testing.T) {
	m := newMgr(t)
	code := m.StartPairing(time.Minute)
	pair, err := m.RedeemPairing(code, "phone", "iPhone")
	if err != nil {
		t.Fatalf("redeem: %v", err)
	}
	if pair.Access == "" || pair.Refresh == "" {
		t.Fatal("empty tokens")
	}
	if _, err := m.VerifyAccess(pair.Access); err != nil {
		t.Fatalf("fresh access should verify: %v", err)
	}
	// The code is single-use.
	if _, err := m.RedeemPairing(code, "x", "y"); err == nil {
		t.Fatal("second redemption of a single-use code must fail")
	}
}

func TestPairingBruteForceLockout(t *testing.T) {
	m := newMgr(t)
	code := m.StartPairing(time.Minute)
	for i := 0; i < maxPairingAttempts; i++ {
		if _, err := m.RedeemPairing("000000", "x", "y"); err == nil {
			t.Fatal("wrong code must fail")
		}
	}
	// After maxPairingAttempts failures the outstanding code is invalidated.
	if _, err := m.RedeemPairing(code, "x", "y"); err == nil {
		t.Fatal("brute-force lockout should invalidate the outstanding code")
	}
}

func TestPairingExpiry(t *testing.T) {
	m := newMgr(t)
	code := m.StartPairing(10 * time.Millisecond)
	time.Sleep(40 * time.Millisecond)
	if _, err := m.RedeemPairing(code, "x", "y"); err == nil {
		t.Fatal("expired code must fail")
	}
}

func TestRefreshAccess(t *testing.T) {
	m := newMgr(t)
	code := m.StartPairing(time.Minute)
	pair, _ := m.RedeemPairing(code, "p", "t")
	newAccess, err := m.RefreshAccess(pair.Refresh)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if _, err := m.VerifyAccess(newAccess); err != nil {
		t.Fatalf("refreshed access invalid: %v", err)
	}
}

func TestPerDeviceRevoke(t *testing.T) {
	m := newMgr(t)
	var hooked string
	m.SetRevokeHook(func(cid string) { hooked = cid })

	code := m.StartPairing(time.Minute)
	pair, _ := m.RedeemPairing(code, "p", "t")
	cid := m.ClientIDFromAccess(pair.Access)
	if cid == "" {
		t.Fatal("access token carries no client id")
	}
	if !m.DeviceActive(cid) {
		t.Fatal("device should be active right after pairing")
	}
	if err := m.RevokeDevice(cid); err != nil {
		t.Fatal(err)
	}
	if hooked != cid {
		t.Fatalf("revoke hook got %q, want %q", hooked, cid)
	}
	if m.DeviceActive(cid) {
		t.Fatal("device should be inactive after revoke")
	}
	if _, err := m.VerifyAccess(pair.Access); err == nil {
		t.Fatal("revoked device's access token must be rejected")
	}
	if _, err := m.RefreshAccess(pair.Refresh); err == nil {
		t.Fatal("revoked device's refresh token must be rejected")
	}
}

func TestGlobalRevoke(t *testing.T) {
	m := newMgr(t)
	code := m.StartPairing(time.Minute)
	pair, _ := m.RedeemPairing(code, "p", "t")
	if err := m.Revoke(); err != nil {
		t.Fatal(err)
	}
	if _, err := m.RefreshAccess(pair.Refresh); err == nil {
		t.Fatal("global revoke must reject refresh tokens from the old generation")
	}
}

func TestVerifyRejectsForgedTokens(t *testing.T) {
	m := newMgr(t)
	if _, err := m.VerifyAccess("not.a.jwt"); err == nil {
		t.Fatal("garbage token must be rejected")
	}
	// alg=none forgery (unsigned token) must be rejected.
	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	body := base64.RawURLEncoding.EncodeToString([]byte(`{"prof":"admin","cid":"x"}`))
	if _, err := m.VerifyAccess(hdr + "." + body + "."); err == nil {
		t.Fatal("alg=none token must be rejected")
	}
}
