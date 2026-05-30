// Package server is the WebSocket hub: it accepts connections, runs the auth
// gate, and routes decoded messages to the per-channel handlers.
//
// One http.Server fronts a single WebSocket endpoint. Every accepted socket
// becomes a conn (implementing channel.Conn) that owns its read loop. Until a
// connection authenticates, only ChCtrl envelopes are processed (pairing, auth,
// refresh, ping); everything else is rejected with an error envelope. Once
// authed, text frames are decoded to protocol.Envelope and routed by Ch to the
// matching channel.Handler, and binary frames are routed to the term handler
// (the one implementing channel.BinaryHandler).
//
// Handlers are built fresh per connection via makeHandlers so per-conn state
// (e.g. a term handler's PTYs) is isolated and torn down on disconnect.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/famgia/remote-host/internal/auth"
	"github.com/famgia/remote-host/internal/channel"
	"github.com/famgia/remote-host/internal/launcher"
	"github.com/famgia/remote-host/internal/protocol"
)

// Server is the WebSocket hub. It is safe to use a single Server for many
// concurrent connections; per-connection state lives on conn.
type Server struct {
	mgr          *auth.Manager
	makeHandlers func() []channel.Handler

	// Live authenticated connections indexed by client id, so a revoked device
	// can be kicked immediately (its sockets closed) instead of lingering.
	connMu sync.Mutex
	conns  map[string]map[*conn]struct{}
}

// New builds a Server. mgr is the shared auth manager (identity, pairing,
// tokens). makeHandlers is invoked once per connection to produce a fresh set
// of per-connection handlers.
func New(mgr *auth.Manager, makeHandlers func() []channel.Handler) *Server {
	s := &Server{
		mgr:          mgr,
		makeHandlers: makeHandlers,
		conns:        make(map[string]map[*conn]struct{}),
	}
	// When a device is revoked (dashboard "Kick", `revoke` CLI), close its live
	// connections so the client drops to the login screen instead of staying on
	// an already-authenticated socket.
	mgr.SetRevokeHook(s.kickClient)
	return s
}

// register records an authenticated connection under its client id.
func (s *Server) register(clientID string, c *conn) {
	if clientID == "" {
		return
	}
	s.connMu.Lock()
	defer s.connMu.Unlock()
	m := s.conns[clientID]
	if m == nil {
		m = make(map[*conn]struct{})
		s.conns[clientID] = m
	}
	m[c] = struct{}{}
}

// deregister drops a connection from the client-id index (on disconnect).
func (s *Server) deregister(c *conn) {
	clientID := c.getClientID()
	if clientID == "" {
		return
	}
	s.connMu.Lock()
	defer s.connMu.Unlock()
	if m := s.conns[clientID]; m != nil {
		delete(m, c)
		if len(m) == 0 {
			delete(s.conns, clientID)
		}
	}
}

// kickClient closes every live connection belonging to clientID. Invoked by the
// auth manager's revoke hook.
func (s *Server) kickClient(clientID string) {
	s.connMu.Lock()
	var victims []*conn
	for c := range s.conns[clientID] {
		victims = append(victims, c)
	}
	s.connMu.Unlock()
	for _, c := range victims {
		// CloseNow drops the TCP connection immediately. We don't use the
		// graceful Close handshake here: it blocks waiting for the peer to echo
		// the close frame, which a kicked (possibly stalled) client may never do.
		_ = c.ws.CloseNow()
	}
}

// bindClient associates a freshly authenticated connection with its device
// client id (from the access token) so a revoke can kick it.
func (s *Server) bindClient(c *conn, access string) {
	cid := s.mgr.ClientIDFromAccess(access)
	c.setClientID(cid)
	s.register(cid, c)
}

// ListenAndServe runs an http.Server on addr that upgrades requests to
// WebSocket and serves each connection until it closes. It returns when the
// underlying http.Server stops; cancelling ctx triggers a graceful shutdown.
func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			// The mobile app connects from any origin; auth happens in-band
			// via the pairing/token handshake, not via the Origin header.
			InsecureSkipVerify: true,
		})
		if err != nil {
			return
		}
		s.serveConn(ctx, c)
	})

	httpSrv := &http.Server{Addr: addr, Handler: mux}

	// Tie shutdown to ctx: when the caller cancels, gracefully stop the server
	// so ListenAndServe can return.
	go func() {
		<-ctx.Done()
		// Best-effort graceful shutdown; ignore the (typically context) error.
		_ = httpSrv.Shutdown(context.Background())
	}()

	err := httpSrv.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// serveConn drives a single WebSocket connection: auth gate, read loop, and
// handler teardown on disconnect.
func (s *Server) serveConn(ctx context.Context, ws *websocket.Conn) {
	// Build a fresh per-connection handler set and index it by channel.
	handlers := s.makeHandlers()
	byChannel := make(map[protocol.Channel]channel.Handler, len(handlers))
	for _, h := range handlers {
		byChannel[h.Channel()] = h
	}

	c := &conn{ws: ws}

	// Always release per-connection handler resources on the way out.
	defer func() {
		s.deregister(c)
		for _, h := range handlers {
			_ = h.Close()
		}
		_ = ws.Close(websocket.StatusNormalClosure, "")
	}()

	for {
		typ, data, err := ws.Read(ctx)
		if err != nil {
			// Normal closure, EOF, or context cancellation: end the loop.
			return
		}

		switch typ {
		case websocket.MessageText:
			var env protocol.Envelope
			if err := json.Unmarshal(data, &env); err != nil {
				_ = c.Send(protocol.ErrorEnvelope(protocol.ChCtrl, "error", "", "bad envelope"))
				continue
			}
			s.handleText(ctx, c, byChannel, env)

		case websocket.MessageBinary:
			// Binary frames only carry stream data, which requires auth.
			if !c.authed() {
				_ = c.Send(protocol.ErrorEnvelope(protocol.ChCtrl, "error", "", "not authenticated"))
				continue
			}
			s.handleBinary(ctx, c, byChannel, data)
		}
	}
}

// handleText processes one decoded text envelope, enforcing the auth gate.
func (s *Server) handleText(ctx context.Context, c *conn, byChannel map[protocol.Channel]channel.Handler, env protocol.Envelope) {
	// Auth gate: before authentication only ctrl messages are honored.
	if !c.authed() {
		if env.Ch != protocol.ChCtrl {
			_ = c.Send(protocol.ErrorEnvelope(env.Ch, "error", env.ID, "not authenticated"))
			return
		}
		s.handleCtrl(c, env)
		return
	}

	// Authenticated: ctrl messages (ping, re-auth, refresh) are still handled
	// by the server itself; everything else routes to a channel handler.
	if env.Ch == protocol.ChCtrl {
		s.handleCtrl(c, env)
		return
	}

	h, ok := byChannel[env.Ch]
	if !ok {
		_ = c.Send(protocol.ErrorEnvelope(env.Ch, "error", env.ID, "unknown channel"))
		return
	}
	if err := h.Handle(ctx, env, c); err != nil {
		_ = c.Send(protocol.ErrorEnvelope(env.Ch, "error", env.ID, err.Error()))
	}
}

// handleBinary decodes a binary frame and routes it to the term handler (the
// one implementing channel.BinaryHandler).
func (s *Server) handleBinary(ctx context.Context, c *conn, byChannel map[protocol.Channel]channel.Handler, data []byte) {
	f, err := protocol.DecodeBinaryFrame(data)
	if err != nil {
		_ = c.Send(protocol.ErrorEnvelope(protocol.ChTerm, "error", "", err.Error()))
		return
	}
	h, ok := byChannel[protocol.ChTerm]
	if !ok {
		return
	}
	bh, ok := h.(channel.BinaryHandler)
	if !ok {
		return
	}
	if err := bh.HandleBinary(ctx, f, c); err != nil {
		_ = c.Send(protocol.ErrorEnvelope(protocol.ChTerm, "error", "", err.Error()))
	}
}

// ---- ctrl channel (auth gate) ----------------------------------------------

// ctrl payload shapes.
type (
	pairReq struct {
		Code     string `json:"code"`
		Name     string `json:"name"`     // device display name (e.g. "Satoshi's iPhone")
		Platform string `json:"platform"` // device type (e.g. "iPhone 15 · iOS 18")
	}
	authReq struct {
		Access string `json:"access"`
	}
	refreshReq struct {
		Refresh string `json:"refresh"`
	}

	pairedResp struct {
		Access   string `json:"access"`
		Refresh  string `json:"refresh"`
		DeviceID string `json:"deviceId"` // lets the client identify/save this host
	}
	okResp struct {
		DeviceID string `json:"deviceId"`
	}
	refreshedResp struct {
		Access string `json:"access"`
	}
)

// handleCtrl processes a ChCtrl envelope: pairing, auth, refresh, ping.
func (s *Server) handleCtrl(c *conn, env protocol.Envelope) {
	switch env.Type {
	case "pair":
		var req pairReq
		if err := env.Bind(&req); err != nil {
			s.ctrlErr(c, env.ID, "bad pair request")
			return
		}
		pair, err := s.mgr.RedeemPairing(req.Code, req.Name, req.Platform)
		if err != nil {
			s.ctrlErr(c, env.ID, err.Error())
			return
		}
		// A redeemed pairing yields a token pair and immediately authenticates
		// this connection via the freshly minted access token.
		prof, err := s.mgr.VerifyAccess(pair.Access)
		if err != nil {
			s.ctrlErr(c, env.ID, err.Error())
			return
		}
		c.setProfile(prof)
		s.bindClient(c, pair.Access)
		s.ctrlReply(c, "paired", env.ID, pairedResp{
			Access: pair.Access, Refresh: pair.Refresh, DeviceID: s.mgr.DeviceID(),
		})

	case "auth":
		var req authReq
		if err := env.Bind(&req); err != nil {
			s.ctrlErr(c, env.ID, "bad auth request")
			return
		}
		prof, err := s.mgr.VerifyAccess(req.Access)
		if err != nil {
			s.ctrlErr(c, env.ID, err.Error())
			return
		}
		c.setProfile(prof)
		s.bindClient(c, req.Access)
		s.ctrlReply(c, "ok", env.ID, okResp{DeviceID: s.mgr.DeviceID()})

	case "refresh":
		var req refreshReq
		if err := env.Bind(&req); err != nil {
			s.ctrlErr(c, env.ID, "bad refresh request")
			return
		}
		access, err := s.mgr.RefreshAccess(req.Refresh)
		if err != nil {
			s.ctrlErr(c, env.ID, err.Error())
			return
		}
		s.ctrlReply(c, "refreshed", env.ID, refreshedResp{Access: access})

	case "ping":
		s.ctrlReply(c, "pong", env.ID, nil)

	default:
		s.ctrlErr(c, env.ID, fmt.Sprintf("unknown ctrl type %q", env.Type))
	}
}

// ctrlReply sends a ctrl response envelope, falling back to an error envelope
// if the payload fails to encode.
func (s *Server) ctrlReply(c *conn, typ, id string, data any) {
	env, err := protocol.NewEnvelope(protocol.ChCtrl, typ, id, data)
	if err != nil {
		s.ctrlErr(c, id, err.Error())
		return
	}
	_ = c.Send(env)
}

// ctrlErr sends a ctrl error envelope correlated to id.
func (s *Server) ctrlErr(c *conn, id, msg string) {
	_ = c.Send(protocol.ErrorEnvelope(protocol.ChCtrl, "error", id, msg))
}

// ---- conn (channel.Conn implementation) -------------------------------------

// conn is the server-side view of one WebSocket connection. It implements
// channel.Conn. WebSocket writes are serialized by mu because coder/websocket
// forbids concurrent writes on a single connection.
type conn struct {
	ws *websocket.Conn

	mu sync.Mutex // guards all websocket writes

	profMu   sync.RWMutex
	profile  launcher.Profile
	auth     bool
	clientID string // device client id (for revoke-kick); "" until authenticated
}

// writeTimeout bounds a single websocket write. Without it, a write to a stalled
// client (full TCP receive window) would block forever while holding c.mu, and
// the read-loop goroutine — which also needs c.mu to send — would deadlock until
// an OS-level TCP timeout. With it, a stuck write aborts (closing the slow
// connection) and releases the lock instead of wedging the whole connection.
const writeTimeout = 10 * time.Second

// Send writes a JSON envelope as a text frame. Safe for concurrent use.
func (c *conn) Send(e protocol.Envelope) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
	defer cancel()
	return c.ws.Write(ctx, websocket.MessageText, b)
}

// SendBinary writes a binary frame. Safe for concurrent use.
func (c *conn) SendBinary(f protocol.BinaryFrame) error {
	b := f.Encode()
	c.mu.Lock()
	defer c.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
	defer cancel()
	return c.ws.Write(ctx, websocket.MessageBinary, b)
}

// Profile returns the authenticated profile (zero value until authenticated).
func (c *conn) Profile() launcher.Profile {
	c.profMu.RLock()
	defer c.profMu.RUnlock()
	return c.profile
}

// setProfile records the authenticated profile and marks the conn authed.
func (c *conn) setProfile(p launcher.Profile) {
	c.profMu.Lock()
	defer c.profMu.Unlock()
	c.profile = p
	c.auth = true
}

// authed reports whether the connection has authenticated.
func (c *conn) authed() bool {
	c.profMu.RLock()
	defer c.profMu.RUnlock()
	return c.auth
}

// setClientID records the device client id this connection authenticated as.
func (c *conn) setClientID(id string) {
	c.profMu.Lock()
	defer c.profMu.Unlock()
	c.clientID = id
}

// getClientID returns the device client id ("" until authenticated).
func (c *conn) getClientID() string {
	c.profMu.RLock()
	defer c.profMu.RUnlock()
	return c.clientID
}
