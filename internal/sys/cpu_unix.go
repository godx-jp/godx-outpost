//go:build darwin || linux || freebsd || openbsd || netbsd

package sys

import (
	"bytes"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

// cpuSample holds a single snapshot of aggregate CPU ticks (Linux /proc/stat).
type cpuSample struct {
	total float64
	idle  float64
}

var (
	cpuMu   sync.Mutex
	lastCPU *cpuSample
)

// cpuPercent returns the overall CPU usage percentage (0–100).
//
//   - Linux: delta of the aggregate "cpu" line in /proc/stat between calls —
//     accurate, pure-Go, no cgo.
//   - macOS / BSD: there is no cheap pure-Go tick source (kern.cpuload does not
//     exist on Darwin, and gopsutil/cpu requires cgo on Darwin), so we sum the
//     per-process %CPU reported by `ps` and normalise by the logical CPU count.
//     `ps` is already used for the process list and is available everywhere.
func cpuPercent() float64 {
	if runtime.GOOS == "linux" {
		return cpuPercentProcStat()
	}
	return cpuPercentPS()
}

// cpuPercentProcStat computes usage from the delta of /proc/stat (Linux).
func cpuPercentProcStat() float64 {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0
	}
	// First line: "cpu  user nice system idle iowait irq softirq steal ...".
	nl := bytes.IndexByte(data, '\n')
	if nl < 0 {
		nl = len(data)
	}
	fields := strings.Fields(string(data[:nl]))
	if len(fields) < 5 || fields[0] != "cpu" {
		return 0
	}
	var total, idle float64
	for i, f := range fields[1:] {
		v, perr := strconv.ParseFloat(f, 64)
		if perr != nil {
			continue
		}
		total += v
		if i == 3 || i == 4 { // idle + iowait
			idle += v
		}
	}

	curr := &cpuSample{total: total, idle: idle}
	cpuMu.Lock()
	prev := lastCPU
	lastCPU = curr
	cpuMu.Unlock()

	if prev == nil {
		return 0 // first sample; next tick yields a real delta
	}
	totalDelta := curr.total - prev.total
	idleDelta := curr.idle - prev.idle
	if totalDelta <= 0 {
		return 0
	}
	return clampPct((1 - idleDelta/totalDelta) * 100)
}

// cpuPercentPS sums per-process %CPU from `ps` and divides by the logical CPU
// count to yield an overall 0–100 figure (macOS / BSD). `ps` %cpu is a decaying
// average, so this tracks sustained load rather than instantaneous spikes.
func cpuPercentPS() float64 {
	out, err := exec.Command("ps", "-A", "-o", "pcpu").Output()
	if err != nil {
		return 0
	}
	var sum float64
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		v, perr := strconv.ParseFloat(strings.TrimSpace(line), 64)
		if perr != nil {
			continue // header ("%CPU") or blank
		}
		sum += v
	}
	n := runtime.NumCPU()
	if n < 1 {
		n = 1
	}
	return clampPct(sum / float64(n))
}

func clampPct(p float64) float64 {
	if p < 0 {
		return 0
	}
	if p > 100 {
		return 100
	}
	return p
}
