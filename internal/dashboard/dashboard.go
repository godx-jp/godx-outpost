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
	"encoding/base64"
	"encoding/json"
	"html/template"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"rsc.io/qr"
)

// DeviceInfo / SessionInfo are the rows the dashboard renders (decoupled from
// the auth/term packages — cmd supplies them via the closures below).
type DeviceInfo struct {
	ClientID string
	Name     string
	Type     string
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
}

// Server renders the dashboard. cmd wires the closures so we avoid importing
// auth/term/store here (no cycles).
type Server struct {
	DeviceID     string
	AdvertiseURL string                       // URL embedded in the QR
	NewCode      func() string                // mint a fresh pairing code
	Devices      func() ([]DeviceInfo, error) // paired clients
	Sessions     func() []SessionInfo         // live terminal sessions
	Revoke       func(clientID string) error  // kick a device
	Rename       func(clientID, name string) error
	Term         TermAttacher                 // nil → no web terminal

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
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/qr.png", s.handleQR)
	mux.HandleFunc("/api/code", s.handleCode)
	mux.HandleFunc("/api/revoke", s.handleRevoke)
	mux.HandleFunc("/api/rename", s.handleRename)
	mux.HandleFunc("/term", s.handleTerm)
	mux.HandleFunc("/term/ws", s.handleTermWS)

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

// handleCode returns the current pairing code as JSON (the page polls this and
// reloads the QR when the code changes). ?new=1 forces a fresh code.
func (s *Server) handleCode(w http.ResponseWriter, r *http.Request) {
	code := s.currentCode(r.URL.Query().Get("new") != "")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"code": code, "url": s.AdvertiseURL, "deviceID": s.DeviceID,
	})
}

// handleRevoke kicks a device: /api/revoke?id=<clientId>.
func (s *Server) handleRevoke(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id != "" && s.Revoke != nil {
		_ = s.Revoke(id)
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// handleRename sets a device's name: /api/rename?id=<clientId>&name=<name>.
func (s *Server) handleRename(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	name := r.URL.Query().Get("name")
	if id != "" && s.Rename != nil {
		_ = s.Rename(id, name)
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
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

// handleTerm serves the xterm.js page that attaches to one session over a local
// WebSocket. Reachable only from loopback (the localOnly wrapper).
func (s *Server) handleTerm(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" || s.Term == nil {
		http.Error(w, "no terminal", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = termTmpl.Execute(w, map[string]any{"ID": id})
}

// handleTermWS bridges the browser xterm to a session: it attaches via s.Term,
// streams output as binary frames, and applies input/resize from JSON text
// frames ({"t":"i","d":<base64>} and {"t":"r","c":cols,"r":rows}).
func (s *Server) handleTermWS(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" || s.Term == nil {
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
	sess, scrollback, err := s.Term.Attach(id,
		func(p []byte) {
			b := append([]byte(nil), p...)
			select {
			case out <- b:
			case <-ctx.Done():
			}
		},
		cancel, // onExit
	)
	if err != nil {
		c.Close(websocket.StatusInternalError, "attach failed")
		return
	}
	defer sess.Detach()

	// Writer goroutine: scrollback first, then live output as binary frames.
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

	// Reader loop: input + resize.
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

var termTmpl = template.Must(template.New("term").Parse(`<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>session {{.ID}}</title>
<meta name="viewport" content="width=device-width, initial-scale=1">
<link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/xterm@5.3.0/css/xterm.css"/>
<style>html,body{margin:0;height:100%;background:#0d0d0d}#t{height:100vh}
 .xterm-viewport{-webkit-overflow-scrolling:touch}</style>
</head><body><div id="t"></div>
<script src="https://cdn.jsdelivr.net/npm/xterm@5.3.0/lib/xterm.js"></script>
<script src="https://cdn.jsdelivr.net/npm/xterm-addon-fit@0.8.0/lib/xterm-addon-fit.js"></script>
<script src="https://cdn.jsdelivr.net/npm/xterm-addon-webgl@0.16.0/lib/xterm-addon-webgl.js"></script>
<script src="https://cdn.jsdelivr.net/npm/xterm-addon-canvas@0.5.0/lib/xterm-addon-canvas.js"></script>
<script>
 var id = "{{.ID}}";
 var term = new Terminal({theme:{background:'#0d0d0d',foreground:'#e0e0e0',cursor:'#4fc3f7'},
   fontFamily:'Menlo,Monaco,"Courier New",monospace',fontSize:14,cursorBlink:true,
   scrollback:5000,smoothScrollDuration:0});
 var fit = new FitAddon.FitAddon(); term.loadAddon(fit);
 term.open(document.getElementById('t'));
 // GPU/Canvas renderer instead of the default DOM renderer (smoother render +
 // scroll). Prefer WebGL; fall back to Canvas, then DOM if neither loads.
 try { var gl = new WebglAddon.WebglAddon(); gl.onContextLoss(function(){ try{gl.dispose();}catch(_){} }); term.loadAddon(gl); }
 catch(_) { try { term.loadAddon(new CanvasAddon.CanvasAddon()); } catch(__){} }
 fit.fit();
 var proto = location.protocol === 'https:' ? 'wss' : 'ws';
 var ws = new WebSocket(proto + '://' + location.host + '/term/ws?id=' + encodeURIComponent(id));
 ws.binaryType = 'arraybuffer';
 function send(o){ if(ws.readyState === 1) ws.send(JSON.stringify(o)); }
 function enc(s){ return btoa(unescape(encodeURIComponent(s))); }
 ws.onopen = function(){ send({t:'r', c:term.cols, r:term.rows}); term.focus(); };
 // Coalesce a burst of output frames into one term.write per animation frame.
 var pending = [], raf = null;
 function flush(){ raf = null; if(!pending.length) return; var chunks = pending; pending = [];
   var total = 0, i; for(i=0;i<chunks.length;i++) total += chunks[i].length;
   var merged = new Uint8Array(total), off = 0;
   for(i=0;i<chunks.length;i++){ merged.set(chunks[i], off); off += chunks[i].length; }
   term.write(merged); }
 ws.onmessage = function(e){
   pending.push(typeof e.data === 'string' ? new TextEncoder().encode(e.data) : new Uint8Array(e.data));
   if(raf === null) raf = requestAnimationFrame(flush); };
 ws.onclose = function(){ term.write('\r\n\x1b[90m[disconnected]\x1b[0m\r\n'); };
 term.onData(function(d){ send({t:'i', d:enc(d)}); });
 window.addEventListener('resize', function(){ fit.fit(); send({t:'r', c:term.cols, r:term.rows}); });
</script></body></html>`))

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
 .empty{color:#555;padding:8px} .kick{color:#ef5350;text-decoration:none;font-size:12px}
 .link{color:#4fc3f7;text-decoration:none;font-size:12px}
 #newbtn{border:none;cursor:pointer}
</style></head><body>
 <h1>hostd dashboard</h1>
 <div class="sub">device {{.DeviceID}} · {{.URL}}</div>

 <div class="card"><div class="qr">
   <img id="qr" src="/qr.png?t={{.Now}}" alt="pairing QR">
   <div>
     <div class="muted">Scan in the app (Hosts → Scan QR) — the QR contains the URL + code:</div>
     <div style="margin:8px 0"><span class="mono muted">{{.URL}}</span></div>
     <div class="code" id="code">{{.Code}}</div>
     <div class="muted" id="rotate" style="margin-bottom:6px"></div>
     <button class="btn" id="newbtn">Refresh code</button>
   </div>
 </div></div>
 <script>
   var lastCode = {{.Code}} + '';
   var nextAt = Date.now() + 60000;
   function paint(code){
     if(code !== lastCode){ lastCode = code; document.getElementById('code').textContent = code;
       document.getElementById('qr').src = '/qr.png?t=' + Date.now(); nextAt = Date.now() + 60000; }
   }
   async function poll(force){
     try{ var r = await fetch('/api/code' + (force?'?new=1':'')); var j = await r.json(); paint(j.code); }catch(e){}
   }
   document.getElementById('newbtn').onclick = function(){ nextAt = Date.now()+60000; poll(true); };
   setInterval(function(){ poll(false); }, 5000);              // pick up the 60s auto-rotation
   setInterval(function(){                                      // countdown label
     var s = Math.max(0, Math.round((nextAt - Date.now())/1000));
     document.getElementById('rotate').textContent = 'auto-refreshes in ' + s + 's';
   }, 1000);
 </script>

 <div class="card">
   <h1 style="font-size:15px">Sessions ({{len .Sessions}})</h1>
   <table><tr><th>id</th><th>title</th><th>folder</th><th>state</th><th></th></tr>
   {{range .Sessions}}<tr><td class="mono">{{.ID}}</td><td>{{.Title}}</td><td class="mono muted">{{.Cwd}}</td>
     <td>{{if .Alive}}<span class="ok">alive</span>{{else}}<span class="off">stopped</span>{{end}}</td>
     <td>{{if .Alive}}<a class="btn" style="margin:0;padding:4px 12px;font-size:12px" href="/term?id={{.ID}}" target="_blank">open ▸</a>{{end}}</td></tr>
   {{else}}<tr><td colspan="5" class="empty">No sessions.</td></tr>{{end}}
   </table>
 </div>

 <div class="card">
   <h1 style="font-size:15px">Paired devices ({{len .Devices}})</h1>
   <table><tr><th>name</th><th>type</th><th>last seen</th><th>status</th><th></th></tr>
   {{range .Devices}}<tr>
     <td>{{if .Name}}{{.Name}}{{else}}<span class="muted">(unnamed)</span>{{end}}
       <a class="link" href="#" onclick="renameDev('{{.ClientID}}','{{.Name}}');return false">✎</a></td>
     <td class="muted">{{if .Type}}{{.Type}}{{else}}—{{end}}</td>
     <td class="muted">{{.LastSeen}}</td>
     <td>{{if eq .Status "active"}}<span class="ok">active</span>{{else}}<span class="off">revoked</span>{{end}}</td>
     <td>{{if eq .Status "active"}}<a class="kick" href="/api/revoke?id={{.ClientID}}" onclick="return confirm('Kick this device? It must re-pair.')">Kick</a>{{end}}</td>
   </tr>{{else}}<tr><td colspan="5" class="empty">No paired devices yet.</td></tr>{{end}}
   </table>
 </div>
 <script>
   function renameDev(id, cur){
     var n = prompt('Device name:', cur || '');
     if(n !== null) location.href = '/api/rename?id=' + encodeURIComponent(id) + '&name=' + encodeURIComponent(n);
   }
 </script>
 <div class="muted">Refresh the page to update. Dashboard is local-only (127.0.0.1).</div>
</body></html>`))
