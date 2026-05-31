package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/godx-jp/godx-outpost/internal/auth"
	"github.com/godx-jp/godx-outpost/internal/channel"
	"github.com/godx-jp/godx-outpost/internal/protocol"
)

// newTestServer wires a real Server (with a temp auth manager and no channel
// handlers) behind an httptest WebSocket endpoint that runs serveConn.
func newTestServer(t *testing.T) (*Server, *auth.Manager, *httptest.Server) {
	t.Helper()
	mgr, err := auth.LoadFrom(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mgr.Close() })

	s := New(mgr, func() []channel.Handler { return nil })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		s.serveConn(ctx, c)
	}))
	t.Cleanup(ts.Close)
	return s, mgr, ts
}

func dial(t *testing.T, ts *httptest.Server) *websocket.Conn {
	t.Helper()
	url := "ws" + strings.TrimPrefix(ts.URL, "http")
	c, _, err := websocket.Dial(context.Background(), url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close(websocket.StatusNormalClosure, "") })
	return c
}

func send(t *testing.T, c *websocket.Conn, e protocol.Envelope) {
	t.Helper()
	b, _ := json.Marshal(e)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := c.Write(ctx, websocket.MessageText, b); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func recv(t *testing.T, c *websocket.Conn) protocol.Envelope {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, b, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var e protocol.Envelope
	if err := json.Unmarshal(b, &e); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return e
}

func pair(t *testing.T, c *websocket.Conn, mgr *auth.Manager) (access, refresh string) {
	t.Helper()
	code := mgr.StartPairing(time.Minute)
	env, _ := protocol.NewEnvelope(protocol.ChCtrl, "pair", "1", map[string]string{
		"code": code, "name": "tester", "platform": "test",
	})
	send(t, c, env)
	resp := recv(t, c)
	if resp.Type != "paired" {
		t.Fatalf("expected 'paired', got type=%q err=%q", resp.Type, resp.Err)
	}
	var d struct{ Access, Refresh, DeviceId string }
	if err := resp.Bind(&d); err != nil {
		t.Fatal(err)
	}
	if d.Access == "" || d.Refresh == "" {
		t.Fatal("paired response missing tokens")
	}
	return d.Access, d.Refresh
}

// Before authenticating, a non-ctrl message must be rejected by the auth gate.
func TestAuthGateRejectsPreAuth(t *testing.T) {
	_, _, ts := newTestServer(t)
	c := dial(t, ts)
	env, _ := protocol.NewEnvelope(protocol.ChTerm, "list", "9", nil)
	send(t, c, env)
	resp := recv(t, c)
	if resp.Err == "" {
		t.Fatalf("expected an error envelope pre-auth, got %+v", resp)
	}
	if !strings.Contains(resp.Err, "not authenticated") {
		t.Fatalf("expected 'not authenticated', got %q", resp.Err)
	}
}

// A full pair handshake authenticates the connection; ctrl/ping then works.
func TestPairThenPing(t *testing.T) {
	_, mgr, ts := newTestServer(t)
	c := dial(t, ts)
	pair(t, c, mgr)

	env, _ := protocol.NewEnvelope(protocol.ChCtrl, "ping", "2", nil)
	send(t, c, env)
	if resp := recv(t, c); resp.Type != "pong" {
		t.Fatalf("expected pong, got %q", resp.Type)
	}
}

// Revoking a device closes its live socket (the revoke-kick), so the client's
// next read fails.
func TestRevokeKicksLiveConnection(t *testing.T) {
	_, mgr, ts := newTestServer(t)
	c := dial(t, ts)
	access, _ := pair(t, c, mgr)

	cid := mgr.ClientIDFromAccess(access)
	if cid == "" {
		t.Fatal("no client id in access token")
	}
	if err := mgr.RevokeDevice(cid); err != nil { // fires the kick hook
		t.Fatal(err)
	}

	// The server should have closed the socket; a read must now error.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, _, err := c.Read(ctx); err == nil {
		t.Fatal("expected the connection to be closed after revoke")
	}
}
