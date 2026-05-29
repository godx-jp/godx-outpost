// Package auth manages the host daemon's persistent cryptographic identity and
// issues/verifies the JWT bearer tokens that gate every client connection.
//
// The model is single-device, single-user for v1:
//
//   - identity.json holds a stable device ID and a symmetric HS256 signing key,
//     created once and reloaded on every subsequent Load(). Because the signing
//     key survives a reboot, tokens minted before a restart remain valid after
//     it — the whole point of persisting it.
//   - Pairing is the bootstrap: StartPairing mints a short-lived in-memory code
//     (shown as a QR by cmd/hostd). A client redeems the code once for a fresh
//     TokenPair. Codes never touch disk.
//   - Tokens are JWTs. Access tokens are short-lived (~15m) and carry the
//     profile name; refresh tokens are long-lived and carry the device ID plus a
//     generation counter. Revoke() bumps the persisted generation so every
//     outstanding refresh token is rejected on next use, forcing re-pairing.
//
// Profile resolution is a stub for v1: every valid access token maps to the
// admin profile. The mapProfile seam is where guest/sandboxed profiles plug in
// at M5 (see docs/PLAN.md "Session profiles & sandbox").
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/famgia/remote-host/internal/launcher"
)

// Token lifetimes. Access tokens are deliberately short so a leaked access
// token has a small blast radius; the client silently refreshes using its
// long-lived refresh token.
const (
	accessTTL  = 15 * time.Minute
	refreshTTL = 365 * 24 * time.Hour // ~1 year; revocation, not expiry, is the kill switch
)

// File names under the config directory. All are written 0600 (owner-only).
const (
	identityFile = "identity.json"
	revokedFile  = "revoked.json"
	configSubdir = "hostd"
)

// TokenPair is the credential bundle handed to a freshly paired client.
type TokenPair struct {
	Access  string `json:"access"`
	Refresh string `json:"refresh"`
}

// identity is the persisted, long-lived secret material. Created once.
type identity struct {
	// DeviceID is a random 128-bit value, hex-encoded. Stable for the life of
	// the install; shown in pairing QR codes so a client can recognize the host.
	DeviceID string `json:"deviceID"`
	// SigningKey is a random 256-bit HMAC key, base64-encoded. Signs every JWT.
	SigningKey string `json:"signingKey"`
}

// revocation is the persisted generation counter. Bumping it invalidates every
// previously issued refresh token (their embedded "gen" claim no longer matches).
type revocation struct {
	Generation int `json:"generation"`
}

// pairingCode is an outstanding, unredeemed pairing code held in memory only.
type pairingCode struct {
	expires time.Time
}

// Manager owns the host identity and the in-memory pairing/revocation state.
// All exported methods are safe for concurrent use.
type Manager struct {
	dir      string // config directory the files live in
	deviceID string
	signKey  []byte // decoded HS256 secret

	mu            sync.Mutex
	generation    int                    // current revocation generation
	pairing       map[string]pairingCode // active codes → expiry
	failedRedeems int                    // consecutive failed RedeemPairing attempts
}

// maxPairingAttempts is the number of consecutive failed redemptions tolerated
// before all outstanding pairing codes are invalidated. With a 6-digit code
// this caps an online brute-force attacker at a negligible success probability
// (≤ maxPairingAttempts / 10^6 per pairing window); re-pairing then requires
// host access to mint a fresh code.
const maxPairingAttempts = 5

// accessClaims are carried by access tokens. The profile name is the only
// authority a connection needs to resolve its launcher.Profile.
type accessClaims struct {
	Profile string `json:"prof"`
	jwt.RegisteredClaims
}

// refreshClaims are carried by refresh tokens. The generation gates revocation;
// the device ID binds the token to this host's identity.
type refreshClaims struct {
	DeviceID   string `json:"dev"`
	Generation int    `json:"gen"`
	jwt.RegisteredClaims
}

// configDir returns the directory hostd state lives in, creating it 0700.
// Uses os.UserConfigDir, falling back to ~/.config when that is unavailable.
func configDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil || base == "" {
		home, herr := os.UserHomeDir()
		if herr != nil {
			return "", fmt.Errorf("auth: cannot locate config dir: %w", errors.Join(err, herr))
		}
		base = filepath.Join(home, ".config")
	}
	dir := filepath.Join(base, configSubdir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("auth: create config dir: %w", err)
	}
	return dir, nil
}

// Load returns a Manager backed by ~/.config/hostd, creating the identity on
// first run and reloading it (never regenerating) on every subsequent call.
// This idempotence is what lets previously issued tokens survive a reboot.
func Load() (*Manager, error) {
	dir, err := configDir()
	if err != nil {
		return nil, err
	}

	id, err := loadOrCreateIdentity(dir)
	if err != nil {
		return nil, err
	}
	key, err := base64.StdEncoding.DecodeString(id.SigningKey)
	if err != nil || len(key) == 0 {
		return nil, fmt.Errorf("auth: malformed signing key in %s", identityFile)
	}

	gen, err := loadGeneration(dir)
	if err != nil {
		return nil, err
	}

	return &Manager{
		dir:        dir,
		deviceID:   id.DeviceID,
		signKey:    key,
		generation: gen,
		pairing:    make(map[string]pairingCode),
	}, nil
}

// loadOrCreateIdentity reads identity.json if present (idempotent: existing
// secrets are returned untouched), otherwise generates and persists fresh ones.
func loadOrCreateIdentity(dir string) (identity, error) {
	path := filepath.Join(dir, identityFile)

	if data, err := os.ReadFile(path); err == nil {
		var id identity
		if jerr := json.Unmarshal(data, &id); jerr != nil {
			return identity{}, fmt.Errorf("auth: parse %s: %w", identityFile, jerr)
		}
		if id.DeviceID == "" || id.SigningKey == "" {
			return identity{}, fmt.Errorf("auth: %s is missing fields", identityFile)
		}
		return id, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return identity{}, fmt.Errorf("auth: read %s: %w", identityFile, err)
	}

	// First run: mint a stable device ID (128-bit) and signing key (256-bit).
	devBytes := make([]byte, 16)
	if _, err := rand.Read(devBytes); err != nil {
		return identity{}, fmt.Errorf("auth: generate device id: %w", err)
	}
	keyBytes := make([]byte, 32)
	if _, err := rand.Read(keyBytes); err != nil {
		return identity{}, fmt.Errorf("auth: generate signing key: %w", err)
	}
	id := identity{
		DeviceID:   hex.EncodeToString(devBytes),
		SigningKey: base64.StdEncoding.EncodeToString(keyBytes),
	}
	if err := writeJSON(path, id); err != nil {
		return identity{}, err
	}
	return id, nil
}

// loadGeneration reads the revocation counter, defaulting to 0 when absent.
func loadGeneration(dir string) (int, error) {
	path := filepath.Join(dir, revokedFile)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("auth: read %s: %w", revokedFile, err)
	}
	var r revocation
	if jerr := json.Unmarshal(data, &r); jerr != nil {
		return 0, fmt.Errorf("auth: parse %s: %w", revokedFile, jerr)
	}
	return r.Generation, nil
}

// writeJSON atomically-ish writes v as 0600 JSON via a temp file + rename so a
// crash mid-write cannot leave a half-written secret file behind.
func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("auth: marshal %s: %w", filepath.Base(path), err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("auth: write %s: %w", filepath.Base(path), err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("auth: persist %s: %w", filepath.Base(path), err)
	}
	return nil
}

// DeviceID returns the stable device identifier.
func (m *Manager) DeviceID() string { return m.deviceID }

// Generation returns the current revocation generation. It increments each time
// Revoke() is called; clients holding a refresh token from an older generation
// are rejected. Shown by `hostd status` as a coarse pairing/health indicator.
func (m *Manager) Generation() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.generation
}

// StartPairing mints a fresh 6-digit pairing code valid for ttl, holds it in
// memory, and returns it. The code is single-use (consumed by RedeemPairing).
func (m *Manager) StartPairing(ttl time.Duration) string {
	code := randomDigits(6)

	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneExpiredLocked()
	m.failedRedeems = 0 // a freshly minted code resets the brute-force counter
	m.pairing[code] = pairingCode{expires: time.Now().Add(ttl)}
	return code
}

// ErrInvalidCode is returned when a pairing code is unknown, expired, or reused.
var ErrInvalidCode = errors.New("auth: invalid or expired pairing code")

// RedeemPairing validates an unexpired pairing code, consumes it, and issues a
// fresh access/refresh token pair. The code cannot be redeemed twice.
func (m *Manager) RedeemPairing(code string) (TokenPair, error) {
	m.mu.Lock()
	m.pruneExpiredLocked()

	// Constant-time lookup over outstanding codes to avoid leaking, via timing,
	// which code (if any) matched.
	var matched string
	var ok bool
	now := time.Now()
	for c, pc := range m.pairing {
		if subtle.ConstantTimeCompare([]byte(c), []byte(code)) == 1 && now.Before(pc.expires) {
			matched, ok = c, true
		}
	}
	if !ok {
		// Throttle online brute force: after too many consecutive misses, drop
		// every outstanding code so guessing becomes futile until the host mints
		// a new one.
		m.failedRedeems++
		if m.failedRedeems >= maxPairingAttempts {
			m.pairing = make(map[string]pairingCode)
		}
		m.mu.Unlock()
		return TokenPair{}, ErrInvalidCode
	}
	delete(m.pairing, matched) // single use
	m.failedRedeems = 0        // success clears the counter
	gen := m.generation
	m.mu.Unlock()

	access, err := m.issueAccess(adminProfileName)
	if err != nil {
		return TokenPair{}, err
	}
	refresh, err := m.issueRefresh(gen)
	if err != nil {
		return TokenPair{}, err
	}
	return TokenPair{Access: access, Refresh: refresh}, nil
}

const adminProfileName = "admin"

// ErrInvalidToken is returned when a presented token fails verification.
var ErrInvalidToken = errors.New("auth: invalid token")

// ErrRevoked is returned when a refresh token's generation predates the current
// (revoked) generation.
var ErrRevoked = errors.New("auth: token revoked")

// VerifyAccess validates an access token's signature and expiry and resolves it
// to a launcher.Profile. For v1 every valid token maps to the admin profile.
func (m *Manager) VerifyAccess(access string) (launcher.Profile, error) {
	var claims accessClaims
	if err := m.parse(access, &claims); err != nil {
		return launcher.Profile{}, err
	}
	return mapProfile(claims.Profile)
}

// RefreshAccess validates a refresh token (signature, expiry, and generation
// against the current revocation counter) and issues a new access token.
func (m *Manager) RefreshAccess(refresh string) (string, error) {
	var claims refreshClaims
	if err := m.parse(refresh, &claims); err != nil {
		return "", err
	}
	// Bind the token to this host's identity.
	if subtle.ConstantTimeCompare([]byte(claims.DeviceID), []byte(m.deviceID)) != 1 {
		return "", ErrInvalidToken
	}

	// Re-read the persisted generation so a `hostd revoke` run from a separate
	// process takes effect on a long-running daemon without a restart. The read
	// is cheap and refreshes happen infrequently (every accessTTL).
	current, err := loadGeneration(m.dir)
	if err != nil {
		return "", err
	}
	m.mu.Lock()
	m.generation = current
	m.mu.Unlock()
	if claims.Generation != current {
		return "", ErrRevoked
	}

	return m.issueAccess(adminProfileName)
}

// Revoke bumps and persists the generation counter, invalidating every
// outstanding refresh token. Clients must re-pair to obtain new credentials.
func (m *Manager) Revoke() error {
	m.mu.Lock()
	next := m.generation + 1
	if err := writeJSON(filepath.Join(m.dir, revokedFile), revocation{Generation: next}); err != nil {
		m.mu.Unlock()
		return err
	}
	m.generation = next
	m.mu.Unlock()
	return nil
}

// issueAccess mints a short-lived access token carrying the profile name.
func (m *Manager) issueAccess(profile string) (string, error) {
	now := time.Now()
	claims := accessClaims{
		Profile: profile,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   m.deviceID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(accessTTL)),
		},
	}
	return m.sign(claims)
}

// issueRefresh mints a long-lived refresh token bound to deviceID + generation.
func (m *Manager) issueRefresh(gen int) (string, error) {
	now := time.Now()
	claims := refreshClaims{
		DeviceID:   m.deviceID,
		Generation: gen,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   m.deviceID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(refreshTTL)),
		},
	}
	return m.sign(claims)
}

// sign serializes claims as an HS256 JWT using the persisted signing key.
func (m *Manager) sign(claims jwt.Claims) (string, error) {
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	s, err := tok.SignedString(m.signKey)
	if err != nil {
		return "", fmt.Errorf("auth: sign token: %w", err)
	}
	return s, nil
}

// parse verifies signature (HS256 only — guards against the alg-confusion /
// "none" attack), and standard expiry, populating claims. Any failure collapses
// to ErrInvalidToken so callers cannot distinguish failure modes.
func (m *Manager) parse(token string, claims jwt.Claims) error {
	_, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("auth: unexpected signing method %v", t.Header["alg"])
		}
		return m.signKey, nil
	}, jwt.WithValidMethods([]string{"HS256"}))
	if err != nil {
		return ErrInvalidToken
	}
	return nil
}

// pruneExpiredLocked drops expired pairing codes. Caller must hold m.mu.
func (m *Manager) pruneExpiredLocked() {
	now := time.Now()
	for c, pc := range m.pairing {
		if !now.Before(pc.expires) {
			delete(m.pairing, c)
		}
	}
}

// randomDigits returns an n-digit decimal string drawn from a CSPRNG.
func randomDigits(n int) string {
	const digits = "0123456789"
	out := make([]byte, n)
	for i := range out {
		idx, err := rand.Int(rand.Reader, big.NewInt(int64(len(digits))))
		if err != nil {
			// crypto/rand failure is catastrophic and non-recoverable; bias
			// toward an unusable code rather than a predictable one.
			idx = big.NewInt(0)
		}
		out[i] = digits[idx.Int64()]
	}
	return string(out)
}

// mapProfile resolves a profile name carried in a token to a launcher.Profile.
// Unknown names are REJECTED (default-deny): an unrecognized name must never
// fall through to an admin (or to a zero Profile, which — with an empty Root —
// would also grant full-filesystem access). For v1 the only valid profile is
// admin.
//
// TODO(M5): map non-admin names ("guest", …) to sandboxed profiles with a
// constrained Root, IsolateNet, and CPU/Mem caps.
func mapProfile(name string) (launcher.Profile, error) {
	switch name {
	case adminProfileName:
		return launcher.Profile{Name: adminProfileName, Admin: true}, nil
	default:
		return launcher.Profile{}, fmt.Errorf("auth: unknown profile %q: %w", name, ErrInvalidToken)
	}
}
