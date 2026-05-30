// Package sys implements the SYSTEM-MONITOR channel (protocol.ChSys).
//
// It handles three message types:
//
//   - "subscribe"   – starts a background goroutine that pushes "metrics"
//     envelopes on a configurable interval (default 1500 ms).
//   - "unsubscribe" – stops the background goroutine.
//   - "kill"        – sends a signal (or SIGTERM) to a process by PID.
//
// CPU usage and process listing are implemented via golang.org/x/sys/unix to
// avoid transitive dependencies of gopsutil/cpu and gopsutil/process that are
// not present in this module's go.sum.
package sys

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/godx-jp/godx-outpost/internal/channel"
	"github.com/godx-jp/godx-outpost/internal/protocol"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/load"
	"github.com/shirou/gopsutil/v3/mem"
	gnet "github.com/shirou/gopsutil/v3/net"
)

// minInterval is the smallest metrics push interval a client may request; it
// bounds the cost of collectMetrics (which forks `ps` and issues syscalls).
const minInterval = 250 * time.Millisecond

// Handler implements channel.Handler for the sys channel.
type Handler struct {
	mu     sync.Mutex
	cancel context.CancelFunc // non-nil while a subscription is running
}

// New creates a ready-to-use Handler.
func New() *Handler {
	return &Handler{}
}

// Channel satisfies channel.Handler; routes to protocol.ChSys.
func (h *Handler) Channel() protocol.Channel {
	return protocol.ChSys
}

// Close stops any running subscription. Called when the client disconnects.
func (h *Handler) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cancel != nil {
		h.cancel()
		h.cancel = nil
	}
	return nil
}

// ---- inbound message shapes -------------------------------------------------

type subscribeReq struct {
	IntervalMs *int `json:"intervalMs"` // optional; nil → 1500 ms
}

type killReq struct {
	PID    int32  `json:"pid"`
	Signal string `json:"signal"` // optional; "" → SIGTERM
}

// ---- outbound metric shapes -------------------------------------------------

type memStats struct {
	Total uint64  `json:"total"`
	Used  uint64  `json:"used"`
	Pct   float64 `json:"pct"`
}

type swapStats struct {
	Total uint64  `json:"total"`
	Used  uint64  `json:"used"`
	Pct   float64 `json:"pct"`
}

type diskStat struct {
	Path  string  `json:"path"`
	Total uint64  `json:"total"`
	Used  uint64  `json:"used"`
	Pct   float64 `json:"pct"`
}

type netStat struct {
	BytesSent uint64 `json:"bytesSent"`
	BytesRecv uint64 `json:"bytesRecv"`
}

type loadStat struct {
	Load1  float64 `json:"load1"`
	Load5  float64 `json:"load5"`
	Load15 float64 `json:"load15"`
}

type procStat struct {
	PID  int32   `json:"pid"`
	Name string  `json:"name"`
	CPU  float64 `json:"cpu"`
	Mem  float32 `json:"mem"`
}

type metrics struct {
	CPUPct   float64    `json:"cpuPct"`
	Mem      memStats   `json:"mem"`
	Swap     *swapStats `json:"swap,omitempty"`
	Disk     []diskStat `json:"disk"`
	Net      netStat    `json:"net"`
	Load     *loadStat  `json:"load,omitempty"`
	TopProcs []procStat `json:"topProcs"`
	TS       int64      `json:"ts"` // Unix ms
}

// Handle dispatches inbound envelopes to the appropriate handler.
func (h *Handler) Handle(ctx context.Context, e protocol.Envelope, c channel.Conn) error {
	switch e.Type {
	case "subscribe":
		var req subscribeReq
		if err := e.Bind(&req); err != nil {
			return fmt.Errorf("sys: subscribe: %w", err)
		}
		interval := 1500 * time.Millisecond
		if req.IntervalMs != nil && *req.IntervalMs > 0 {
			interval = time.Duration(*req.IntervalMs) * time.Millisecond
		}
		// Clamp to a floor so a client cannot request, say, intervalMs=1 and
		// spin collectMetrics (which forks `ps` and issues many syscalls) ~1000×
		// per second, pegging the host CPU.
		if interval < minInterval {
			interval = minInterval
		}
		h.startSubscription(ctx, interval, c)

	case "unsubscribe":
		h.stopSubscription()

	case "kill":
		var req killReq
		if err := e.Bind(&req); err != nil {
			return fmt.Errorf("sys: kill: %w", err)
		}
		if err := killProcess(req.PID, req.Signal); err != nil {
			return err
		}
		env, err := protocol.NewEnvelope(protocol.ChSys, "kill", e.ID, map[string]bool{"ok": true})
		if err != nil {
			return err
		}
		return c.Send(env)

	default:
		return fmt.Errorf("sys: unknown type %q", e.Type)
	}
	return nil
}

// startSubscription starts (or restarts) the metrics goroutine.
func (h *Handler) startSubscription(parent context.Context, interval time.Duration, c channel.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Cancel any existing subscription first.
	if h.cancel != nil {
		h.cancel()
	}

	ctx, cancel := context.WithCancel(parent)
	h.cancel = cancel

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m, err := collectMetrics()
				if err != nil {
					// Best-effort; skip this tick on error.
					continue
				}
				env, err := protocol.NewEnvelope(protocol.ChSys, "metrics", "", m)
				if err != nil {
					continue
				}
				if err := c.Send(env); err != nil {
					// Connection closed; exit quietly.
					return
				}
			}
		}
	}()
}

// stopSubscription cancels any running subscription goroutine.
func (h *Handler) stopSubscription() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cancel != nil {
		h.cancel()
		h.cancel = nil
	}
}

// collectMetrics gathers a single snapshot of all system metrics.
func collectMetrics() (*metrics, error) {
	m := &metrics{TS: time.Now().UnixMilli()}

	// CPU – platform-specific implementation (avoids gopsutil/cpu darwin deps).
	m.CPUPct = cpuPercent()

	// Memory.
	vm, err := mem.VirtualMemory()
	if err == nil {
		m.Mem = memStats{
			Total: vm.Total,
			Used:  vm.Used,
			Pct:   vm.UsedPercent,
		}
	}

	// Swap.
	sm, err := mem.SwapMemory()
	if err == nil && sm.Total > 0 {
		m.Swap = &swapStats{
			Total: sm.Total,
			Used:  sm.Used,
			Pct:   sm.UsedPercent,
		}
	}

	// Disk – all physical partitions.
	partitions, err := disk.Partitions(false)
	if err == nil {
		for _, part := range partitions {
			usage, uerr := disk.Usage(part.Mountpoint)
			if uerr != nil {
				continue
			}
			m.Disk = append(m.Disk, diskStat{
				Path:  part.Mountpoint,
				Total: usage.Total,
				Used:  usage.Used,
				Pct:   usage.UsedPercent,
			})
		}
	}

	// Network – aggregate across all interfaces.
	counters, err := gnet.IOCounters(false) // false = aggregate
	if err == nil && len(counters) > 0 {
		m.Net = netStat{
			BytesSent: counters[0].BytesSent,
			BytesRecv: counters[0].BytesRecv,
		}
	}

	// Load average (Linux/macOS; absent on Windows).
	avg, err := load.Avg()
	if err == nil {
		m.Load = &loadStat{
			Load1:  avg.Load1,
			Load5:  avg.Load5,
			Load15: avg.Load15,
		}
	}

	// Top 5 processes by CPU – platform-specific implementation.
	m.TopProcs = topProcesses(5)

	return m, nil
}

// Ensure Handler implements channel.Handler at compile time.
var _ channel.Handler = (*Handler)(nil)

// Ensure JSON types are properly declared (compile-time shape check).
var _ = json.Marshal
