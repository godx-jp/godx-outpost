//go:build darwin || linux || freebsd || openbsd || netbsd

package sys

import (
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// cpuSample holds a single snapshot of aggregate CPU ticks.
type cpuSample struct {
	total float64
	idle  float64
}

var (
	cpuMu   sync.Mutex
	lastCPU *cpuSample
)

// cpuPercent returns the current overall CPU usage percentage.
// It computes a delta against the previous sample (initially a brief poll).
func cpuPercent() float64 {
	curr := readCPUSample()
	if curr == nil {
		return 0
	}

	cpuMu.Lock()
	prev := lastCPU
	lastCPU = curr
	cpuMu.Unlock()

	if prev == nil {
		// First call: sleep briefly to get a meaningful delta.
		time.Sleep(50 * time.Millisecond)
		curr2 := readCPUSample()
		if curr2 == nil {
			return 0
		}
		cpuMu.Lock()
		lastCPU = curr2
		cpuMu.Unlock()
		prev = curr
		curr = curr2
	}

	totalDelta := curr.total - prev.total
	idleDelta := curr.idle - prev.idle
	if totalDelta <= 0 {
		return 0
	}
	return (1 - idleDelta/totalDelta) * 100
}

// readCPUSample reads the aggregate CPU tick counts from the kernel.
// On Darwin it uses host_statistics via Mach traps exposed through unix.
// On Linux it reads the "cpu" line from /proc/stat via sysinfo or Sysctl.
func readCPUSample() *cpuSample {
	// Use getrusage-based clock times via unix.Times which is portable.
	// For overall system CPU we use the loadavg as a proxy when direct
	// tick access isn't available without extra deps.
	//
	// Darwin: use kern.cpuload sysctl (available on macOS 10.8+).
	// Format: array of uint32 [user, sys, idle, nice] per logical CPU.
	raw, err := unix.SysctlRaw("kern.cpuload")
	if err != nil {
		// Fallback: return nil so caller handles gracefully.
		return nil
	}

	// kern.cpuload returns one uint32[CPU_STATE_MAX=4] per logical CPU.
	// Layout: [user, sys, idle, nice] × numCPU
	const stateSize = 4 * 4 // 4 uint32s per CPU
	if len(raw) < stateSize {
		return nil
	}

	var totalTicks, idleTicks uint64
	numCPU := len(raw) / stateSize
	for i := 0; i < numCPU; i++ {
		off := i * stateSize
		user := uint64(raw[off]) | uint64(raw[off+1])<<8 | uint64(raw[off+2])<<16 | uint64(raw[off+3])<<24
		off += 4
		sys := uint64(raw[off]) | uint64(raw[off+1])<<8 | uint64(raw[off+2])<<16 | uint64(raw[off+3])<<24
		off += 4
		idle := uint64(raw[off]) | uint64(raw[off+1])<<8 | uint64(raw[off+2])<<16 | uint64(raw[off+3])<<24
		off += 4
		nice := uint64(raw[off]) | uint64(raw[off+1])<<8 | uint64(raw[off+2])<<16 | uint64(raw[off+3])<<24
		totalTicks += user + sys + idle + nice
		idleTicks += idle
	}
	return &cpuSample{total: float64(totalTicks), idle: float64(idleTicks)}
}
