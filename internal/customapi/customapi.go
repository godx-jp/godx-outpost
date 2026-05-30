// Package customapi implements the ChAPI channel, which lets callers register
// named handler functions that are invoked by remote clients via a simple
// request/response protocol.
//
// Protocol (text frames only):
//
//	→  Envelope{Ch:"api", Type:"call", ID:"<id>", Data:{name:"<n>", payload:<raw>}}
//	←  Envelope{Ch:"api", Type:"result", ID:"<id>", Data:{result:<any>}}
//	←  Envelope{Ch:"api", Type:"result", ID:"<id>", Err:"<message>"}   (on failure)
package customapi

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"sync"

	"github.com/godx-jp/godx-outpost/internal/channel"
	"github.com/godx-jp/godx-outpost/internal/protocol"
)

// HandlerFunc is a user-supplied function called when a client invokes the
// named API endpoint. payload is the raw JSON passed by the caller; the return
// value is JSON-marshalled and forwarded as the result field.
type HandlerFunc func(ctx context.Context, payload json.RawMessage) (any, error)

// Registry holds a set of named HandlerFuncs and satisfies channel.Handler so
// the WebSocket server can route ChAPI envelopes to it.
type Registry struct {
	mu       sync.RWMutex
	handlers map[string]HandlerFunc
}

// New creates a Registry pre-loaded with two built-in handlers:
//
//   - "echo"   – returns the caller's payload unchanged.
//   - "whoami" – returns the current OS user, hostname, and process ID.
func New() *Registry {
	r := &Registry{
		handlers: make(map[string]HandlerFunc),
	}

	// echo: reflect the payload back to the caller as-is.
	r.Register("echo", func(_ context.Context, payload json.RawMessage) (any, error) {
		return payload, nil
	})

	// whoami: return basic host identity information.
	r.Register("whoami", func(_ context.Context, _ json.RawMessage) (any, error) {
		u, err := user.Current()
		if err != nil {
			return nil, fmt.Errorf("customapi: whoami: get user: %w", err)
		}
		hostname, err := os.Hostname()
		if err != nil {
			return nil, fmt.Errorf("customapi: whoami: get hostname: %w", err)
		}
		return map[string]any{
			"user": u.Username,
			"host": hostname,
			"pid":  os.Getpid(),
		}, nil
	})

	return r
}

// Register adds or replaces the named handler. It is safe to call concurrently.
func (r *Registry) Register(name string, fn HandlerFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.handlers[name] = fn
}

// Channel returns protocol.ChAPI so the server routes "api" envelopes here.
func (r *Registry) Channel() protocol.Channel {
	return protocol.ChAPI
}

// callRequest is the shape of Envelope.Data for a "call" message.
type callRequest struct {
	Name    string          `json:"name"`
	Payload json.RawMessage `json:"payload"`
}

// resultData is the shape of Envelope.Data in a successful "result" reply.
type resultData struct {
	Result any `json:"result"`
}

// Handle processes an inbound ChAPI envelope.
//
// Recognised type: "call" – looks up the handler by name, executes it, and
// sends a "result" envelope back on c. Unknown types are silently ignored
// (forward-compatibility).
func (r *Registry) Handle(ctx context.Context, e protocol.Envelope, c channel.Conn) error {
	if e.Type != "call" {
		// Unknown type – ignore for forward-compatibility.
		return nil
	}

	var req callRequest
	if err := e.Bind(&req); err != nil {
		reply := protocol.ErrorEnvelope(protocol.ChAPI, "result", e.ID,
			fmt.Sprintf("customapi: bad call data: %v", err))
		return c.Send(reply)
	}

	// Look up the handler while holding only the read lock.
	r.mu.RLock()
	fn, ok := r.handlers[req.Name]
	r.mu.RUnlock()

	if !ok {
		reply := protocol.ErrorEnvelope(protocol.ChAPI, "result", e.ID,
			fmt.Sprintf("customapi: unknown handler %q", req.Name))
		return c.Send(reply)
	}

	// Execute the handler.
	result, err := fn(ctx, req.Payload)
	if err != nil {
		reply := protocol.ErrorEnvelope(protocol.ChAPI, "result", e.ID,
			fmt.Sprintf("customapi: handler %q: %v", req.Name, err))
		return c.Send(reply)
	}

	// Marshal the success response.
	reply, err := protocol.NewEnvelope(protocol.ChAPI, "result", e.ID, resultData{Result: result})
	if err != nil {
		errReply := protocol.ErrorEnvelope(protocol.ChAPI, "result", e.ID,
			fmt.Sprintf("customapi: marshal result: %v", err))
		return c.Send(errReply)
	}
	return c.Send(reply)
}

// Close is a no-op; the registry holds no per-connection state.
func (r *Registry) Close() error {
	return nil
}
