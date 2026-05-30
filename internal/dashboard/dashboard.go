// Package dashboard serves the local web UI for outpost: a React single-page app
// (embedded, built from web/) plus the JSON API + WebSocket it talks to. It shows
// a pairing QR (per local IP or a custom domain), the paired devices, and live
// terminal sessions (with an in-browser xterm).
//
// SECURITY: the dashboard hands out pairing QRs, so it must be reachable only
// from the host itself. It is always bound to 127.0.0.1 (never the public bind
// address), regardless of where the WebSocket server listens.
package dashboard

import (
	"context"
	"embed"
	"encoding/base64"
	"encoding/json"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"rsc.io/qr"
)

//go:embed all:static
var staticFS embed.FS

// shQuote single-quotes a string for a POSIX shell.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// DeviceInfo / SessionInfo are the rows the dashboard renders (decoupled from
// the auth/term packages — cmd supplies them via the closures below).
type DeviceInfo struct {
	ClientID string `json:"clientId"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	PairedAt string `json:"pairedAt"`
	LastSeen string `json:"lastSeen"`
	Status   string `json:"status"`
}

type SessionInfo struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Cwd   string `json:"cwd"`
	Alive bool   `json:"alive"`
	Kind  string `json:"kind"` // "shell" | "tmux" | "zellij"
}

// Target is one address the QR can point at (a LAN IP or a custom domain).
type Target struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

// TermSession is a live attachment to one terminal session (decoupled from the
// term package). Detach is called when the browser disconnects.
type TermSession interface {
	Write(p []byte) error
	Resize(cols, rows uint16) error
	Detach()
}

// TermAttacher bridges the dashboard's local web terminal to the session
// Manager. onOutput/onExit are invoked from a background goroutine; the
// returned scrollback is replayed into the browser first.
type TermAttacher interface {
	Attach(id string, onOutput func([]byte), onExit func()) (sess TermSession, scrollback []byte, err error)
	// AttachNew creates a fresh shell session (labelled title), attaches, and —
	// once the shell settles — types initCmd. Used to open a tmux/zellij session
	// in the browser by running its `attach` command in a new hostd session.
	AttachNew(title, initCmd string, onOutput func([]byte), onExit func()) (sess TermSession, scrollback []byte, err error)
}

// Server renders the dashboard. cmd wires the closures so we avoid importing
// auth/term/store here (no cycles).
type Server struct {
	DeviceID     string
	AdvertiseURL string                       // default URL embedded in the QR
	NewCode      func() string                // mint a fresh pairing code
	Devices      func() ([]DeviceInfo, error) // paired clients
	Sessions     func() []SessionInfo         // live terminal sessions
	Revoke       func(clientID string) error  // kick a device
	Rename       func(clientID, name string) error
	Term         TermAttacher                  // nil → no web terminal
	Kill         func(kind, name string) error // kill a session (shell/tmux/zellij)
	Domain       func() string                 // persisted custom domain ("" = none)
	SetDomain    func(string) error            // persist a custom domain

	mu     sync.Mutex
	code   string    // current pairing code shown in the QR
	codeAt time.Time // when it was minted (for auto-rotation)
}

// rotateEvery is how long a dashboard pairing code stays shown before a fresh
// one is minted automatically (the page polls and updates the QR).
const rotateEvery = time.Minute

// ListenAndServe runs the dashboard on 127.0.0.1:port until ctx is cancelled.
func (s *Server) ListenAndServe(ctx context.Context, port string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/state", s.handleState)
	mux.HandleFunc("/api/code", s.handleCode)
	mux.HandleFunc("/api/domain", s.handleDomain)
	mux.HandleFunc("/api/revoke", s.handleRevoke)
	mux.HandleFunc("/api/rename", s.handleRename)
	mux.HandleFunc("/api/kill-session", s.handleKillSession)
	mux.HandleFunc("/qr.png", s.handleQR)
	mux.HandleFunc("/term/ws", s.handleTermWS)
	mux.Handle("/", s.spaHandler())

	srv := &http.Server{Addr: "127.0.0.1:" + port, Handler: localOnly(mux)}
	go func() {
		<-ctx.Done()
		_ = srv.Shutdown(context.Background())
	}()
	err := srv.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

// spaHandler serves the embedded React build, falling back to index.html so
// client-side routes (e.g. /term) resolve.
func (s *Server) spaHandler() http.Handler {
	sub, _ := fs.Sub(staticFS, "static")
	files := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p != "" && p != "index.html" {
			if f, err := sub.Open(p); err == nil {
				_ = f.Close()
				files.ServeHTTP(w, r)
				return
			}
		}
		idx, err := sub.Open("index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer idx.Close()
		// index.html must never be cached, or browsers keep loading a stale hashed
		// bundle after a redeploy.
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.Copy(w, idx)
	})
}

// localOnly rejects any request that didn't originate from the loopback host.
func localOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.RemoteAddr
		for j := len(host) - 1; j >= 0; j-- {
			if host[j] == ':' {
				host = host[:j]
				break
			}
		}
		if host != "127.0.0.1" && host != "::1" && host != "[::1]" {
			http.Error(w, "dashboard is local-only", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// currentCode returns the active pairing code, minting a fresh one when forced,
// on first use, or once the current one is older than rotateEvery.
func (s *Server) currentCode(force bool) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if force || s.code == "" || time.Since(s.codeAt) >= rotateEvery {
		s.code = s.NewCode()
		s.codeAt = time.Now()
	}
	return s.code
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// handleState returns the full dashboard state for the SPA.
func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	devices, _ := s.Devices()
	if devices == nil {
		devices = []DeviceInfo{}
	}
	sessions := s.Sessions()
	if sessions == nil {
		sessions = []SessionInfo{}
	}
	domain := ""
	if s.Domain != nil {
		domain = s.Domain()
	}
	writeJSON(w, map[string]any{
		"deviceId":  s.DeviceID,
		"code":      s.currentCode(r.URL.Query().Get("new") != ""),
		"advertise": s.AdvertiseURL,
		"domain":    domain,
		"targets":   s.targets(domain),
		"sessions":  sessions,
		"devices":   devices,
	})
}

// handleCode returns just the current pairing code (lightweight poll).
func (s *Server) handleCode(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]string{"code": s.currentCode(r.URL.Query().Get("new") != "")})
}

// handleDomain persists a custom domain: POST /api/domain?domain=<host-or-url>.
func (s *Server) handleDomain(w http.ResponseWriter, r *http.Request) {
	if s.SetDomain != nil {
		_ = s.SetDomain(strings.TrimSpace(r.URL.Query().Get("domain")))
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleRevoke(w http.ResponseWriter, r *http.Request) {
	if id := r.URL.Query().Get("id"); id != "" && s.Revoke != nil {
		_ = s.Revoke(id)
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleRename(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if id := q.Get("id"); id != "" && s.Rename != nil {
		_ = s.Rename(id, q.Get("name"))
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleKillSession(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if name := q.Get("name"); name != "" && s.Kill != nil {
		_ = s.Kill(q.Get("kind"), name)
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// handleQR renders a pairing QR for ?url=<wsURL> (defaults to AdvertiseURL).
func (s *Server) handleQR(w http.ResponseWriter, r *http.Request) {
	target := r.URL.Query().Get("url")
	if target == "" {
		target = s.AdvertiseURL
	}
	payload, _ := json.Marshal(map[string]string{
		"url":         target,
		"deviceID":    s.DeviceID,
		"pairingCode": s.currentCode(false),
	})
	code, err := qr.Encode(string(payload), qr.M)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(code.PNG())
}

// targets lists the addresses the QR can point at: the advertised URL, every
// LAN IPv4 (on the advertised port), and a custom domain if configured.
func (s *Server) targets(domain string) []Target {
	port := "8722"
	if u, err := url.Parse(s.AdvertiseURL); err == nil && u.Port() != "" {
		port = u.Port()
	}
	seen := map[string]bool{}
	var out []Target
	add := func(label, raw string) {
		if raw == "" || seen[raw] {
			return
		}
		seen[raw] = true
		out = append(out, Target{Label: label, URL: raw})
	}
	add("Advertised", s.AdvertiseURL)
	for _, ip := range localIPv4s() {
		add("LAN "+ip, "ws://"+ip+":"+port)
	}
	if domain != "" {
		d := domain
		if !strings.Contains(d, "://") {
			d = "wss://" + d // a custom domain (e.g. Cloudflare tunnel) is TLS
		}
		add("Domain", d)
	}
	return out
}

// localIPv4s returns the host's non-loopback IPv4 addresses.
func localIPv4s() []string {
	var ips []string
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ips
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() || ipnet.IP.IsLinkLocalUnicast() {
			continue // skip loopback and 169.254.x link-local (not pairable)
		}
		if v4 := ipnet.IP.To4(); v4 != nil {
			ips = append(ips, v4.String())
		}
	}
	return ips
}

// handleTermWS bridges the browser xterm to a session: it attaches via s.Term,
// streams output as binary frames, and applies input/resize from JSON text
// frames ({"t":"i","d":<base64>} and {"t":"r","c":cols,"r":rows}).
func (s *Server) handleTermWS(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	id, tool, name := q.Get("id"), q.Get("tool"), q.Get("name")
	if s.Term == nil || (id == "" && name == "") {
		http.Error(w, "no terminal", http.StatusNotFound)
		return
	}
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer c.Close(websocket.StatusNormalClosure, "")

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	out := make(chan []byte, 1024)
	onOut := func(p []byte) {
		b := append([]byte(nil), p...)
		select {
		case out <- b:
		case <-ctx.Done():
		}
	}

	var sess TermSession
	var scrollback []byte
	if id != "" {
		sess, scrollback, err = s.Term.Attach(id, onOut, cancel)
	} else {
		var initCmd string
		switch tool {
		case "tmux":
			initCmd = "tmux attach -t " + shQuote(name)
		case "zellij":
			initCmd = "zellij attach " + shQuote(name)
		default:
			c.Close(websocket.StatusInternalError, "unknown tool")
			return
		}
		sess, scrollback, err = s.Term.AttachNew(name, initCmd, onOut, cancel)
	}
	if err != nil {
		c.Close(websocket.StatusInternalError, "attach failed")
		return
	}
	defer sess.Detach()

	go func() {
		if len(scrollback) > 0 {
			if c.Write(ctx, websocket.MessageBinary, scrollback) != nil {
				cancel()
				return
			}
		}
		for {
			select {
			case <-ctx.Done():
				return
			case b := <-out:
				if c.Write(ctx, websocket.MessageBinary, b) != nil {
					cancel()
					return
				}
			}
		}
	}()

	for {
		typ, data, rerr := c.Read(ctx)
		if rerr != nil {
			break
		}
		if typ != websocket.MessageText {
			continue
		}
		var m struct {
			T string `json:"t"`
			D string `json:"d"`
			C uint16 `json:"c"`
			R uint16 `json:"r"`
		}
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		switch m.T {
		case "i":
			if raw, derr := base64.StdEncoding.DecodeString(m.D); derr == nil {
				_ = sess.Write(raw)
			}
		case "r":
			if m.C > 0 && m.R > 0 {
				_ = sess.Resize(m.C, m.R)
			}
		}
	}
	cancel()
}
