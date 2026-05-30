// Package dashboard serves a small local web UI for hostd: it shows a pairing
// QR (scan it with the phone — no terminal needed), plus the paired devices and
// live terminal sessions.
//
// SECURITY: the dashboard hands out pairing QRs, so it must be reachable only
// from the host itself. It is always bound to 127.0.0.1 (never the public bind
// address), regardless of where the WebSocket server listens.
package dashboard

import (
	"context"
	"encoding/json"
	"html/template"
	"net/http"
	"sync"
	"time"

	"rsc.io/qr"
)

// DeviceInfo / SessionInfo are the rows the dashboard renders (decoupled from
// the auth/term packages — cmd supplies them via the closures below).
type DeviceInfo struct {
	ClientID string
	PairedAt string
	LastSeen string
	Status   string
}

type SessionInfo struct {
	ID    string
	Title string
	Cwd   string
	Alive bool
}

// Server renders the dashboard. cmd wires the closures so we avoid importing
// auth/term/store here (no cycles).
type Server struct {
	DeviceID     string
	AdvertiseURL string                       // URL embedded in the QR
	NewCode      func() string                // mint a fresh pairing code
	Devices      func() ([]DeviceInfo, error) // paired clients
	Sessions     func() []SessionInfo         // live terminal sessions

	mu   sync.Mutex
	code string // current pairing code shown in the QR
}

// ListenAndServe runs the dashboard on 127.0.0.1:port until ctx is cancelled.
func (s *Server) ListenAndServe(ctx context.Context, port string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/qr.png", s.handleQR)

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

// localOnly rejects any request that didn't originate from the loopback host.
func localOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.RemoteAddr
		if i := len(host) - 1; i >= 0 {
			// strip :port
			for j := len(host) - 1; j >= 0; j-- {
				if host[j] == ':' {
					host = host[:j]
					break
				}
			}
		}
		if host != "127.0.0.1" && host != "::1" && host != "[::1]" {
			http.Error(w, "dashboard is local-only", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// currentCode returns the active pairing code, minting one if needed or when
// forced (the "new code" button).
func (s *Server) currentCode(force bool) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if force || s.code == "" {
		s.code = s.NewCode()
	}
	return s.code
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	code := s.currentCode(r.URL.Query().Get("new") != "")
	devices, _ := s.Devices()
	sessions := s.Sessions()

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = indexTmpl.Execute(w, map[string]any{
		"DeviceID": s.DeviceID,
		"URL":      s.AdvertiseURL,
		"Code":     code,
		"Devices":  devices,
		"Sessions": sessions,
		"Now":      time.Now().Unix(), // cache-bust the QR img
	})
}

func (s *Server) handleQR(w http.ResponseWriter, r *http.Request) {
	payload, _ := json.Marshal(map[string]string{
		"url":         s.AdvertiseURL,
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

var indexTmpl = template.Must(template.New("dash").Parse(`<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>hostd</title>
<meta name="viewport" content="width=device-width, initial-scale=1">
<style>
 body{background:#0d0d0d;color:#e0e0e0;font-family:-apple-system,Segoe UI,Roboto,sans-serif;margin:0;padding:24px}
 h1{font-size:20px;margin:0 0 4px} .sub{color:#777;font-size:12px;font-family:monospace;margin-bottom:20px}
 .card{background:#111;border:1px solid #222;border-radius:12px;padding:20px;margin-bottom:16px}
 .qr{display:flex;gap:20px;align-items:center;flex-wrap:wrap}
 .qr img{width:240px;height:240px;background:#fff;border-radius:8px;padding:8px;image-rendering:pixelated}
 .code{font-size:34px;font-weight:700;letter-spacing:6px;color:#4fc3f7;font-family:monospace}
 .muted{color:#888;font-size:13px} a.btn{display:inline-block;margin-top:10px;color:#0d0d0d;background:#4fc3f7;
   padding:8px 16px;border-radius:8px;text-decoration:none;font-weight:600}
 table{width:100%;border-collapse:collapse;font-size:13px} td,th{text-align:left;padding:6px 8px;border-bottom:1px solid #1c1c1c}
 th{color:#777;font-weight:500} .ok{color:#4caf50} .off{color:#ef5350} .mono{font-family:monospace}
 .empty{color:#555;padding:8px}
</style></head><body>
 <h1>hostd dashboard</h1>
 <div class="sub">device {{.DeviceID}} · {{.URL}}</div>

 <div class="card"><div class="qr">
   <img src="/qr.png?t={{.Now}}" alt="pairing QR">
   <div>
     <div class="muted">Scan in the app (Hosts → Scan QR), or enter manually:</div>
     <div style="margin:8px 0"><span class="mono muted">{{.URL}}</span></div>
     <div class="code">{{.Code}}</div>
     <a class="btn" href="/?new=1">New code</a>
   </div>
 </div></div>

 <div class="card">
   <h1 style="font-size:15px">Sessions ({{len .Sessions}})</h1>
   <table><tr><th>id</th><th>title</th><th>folder</th><th>state</th></tr>
   {{range .Sessions}}<tr><td class="mono">{{.ID}}</td><td>{{.Title}}</td><td class="mono muted">{{.Cwd}}</td>
     <td>{{if .Alive}}<span class="ok">alive</span>{{else}}<span class="off">stopped</span>{{end}}</td></tr>
   {{else}}<tr><td colspan="4" class="empty">No sessions.</td></tr>{{end}}
   </table>
 </div>

 <div class="card">
   <h1 style="font-size:15px">Paired devices ({{len .Devices}})</h1>
   <table><tr><th>client id</th><th>paired</th><th>last seen</th><th>status</th></tr>
   {{range .Devices}}<tr><td class="mono">{{.ClientID}}</td><td class="muted">{{.PairedAt}}</td>
     <td class="muted">{{.LastSeen}}</td><td>{{if eq .Status "active"}}<span class="ok">active</span>{{else}}<span class="off">revoked</span>{{end}}</td></tr>
   {{else}}<tr><td colspan="4" class="empty">No paired devices yet.</td></tr>{{end}}
   </table>
 </div>
 <div class="muted">Refresh the page to update. Dashboard is local-only (127.0.0.1).</div>
</body></html>`))
