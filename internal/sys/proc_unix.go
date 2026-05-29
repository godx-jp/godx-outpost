//go:build darwin || linux || freebsd || openbsd || netbsd

package sys

import (
	"bytes"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

// topProcesses returns up to n processes sorted descending by CPU %.
// It shells out to `ps` which is available on all Unix platforms and avoids
// the transitive dependencies of gopsutil/process.
func topProcesses(n int) []procStat {
	// ps aux prints: USER PID %CPU %MEM ... COMMAND
	out, err := exec.Command("ps", "ax", "-o", "pid,pcpu,pmem,comm").Output()
	if err != nil {
		return nil
	}

	type row struct {
		pid  int32
		cpu  float64
		mem  float32
		name string
	}

	lines := bytes.Split(bytes.TrimSpace(out), []byte("\n"))
	rows := make([]row, 0, len(lines))
	for _, line := range lines {
		fields := strings.Fields(string(line))
		if len(fields) < 4 {
			continue
		}
		pid64, err := strconv.ParseInt(fields[0], 10, 32)
		if err != nil {
			continue
		}
		cpu, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			cpu = 0
		}
		mem64, err := strconv.ParseFloat(fields[2], 32)
		if err != nil {
			mem64 = 0
		}
		name := fields[3]
		// Strip path prefix if present.
		if idx := strings.LastIndex(name, "/"); idx >= 0 {
			name = name[idx+1:]
		}
		rows = append(rows, row{
			pid:  int32(pid64),
			cpu:  cpu,
			mem:  float32(mem64),
			name: name,
		})
	}

	sort.Slice(rows, func(i, j int) bool {
		return rows[i].cpu > rows[j].cpu
	})

	if len(rows) > n {
		rows = rows[:n]
	}

	result := make([]procStat, len(rows))
	for i, r := range rows {
		result[i] = procStat{PID: r.pid, Name: r.name, CPU: r.cpu, Mem: r.mem}
	}
	return result
}

// killProcess sends a signal to the given PID.
//
// PIDs ≤ 1 are rejected: syscall.Kill treats 0 as "every process in the caller's
// process group", -1 as "every process the caller may signal", and negative
// values as a whole process group — any of which a remote client could use to
// wipe out the daemon's entire session. PID 1 (init) is likewise off-limits.
func killProcess(pid int32, sigName string) error {
	if pid <= 1 {
		return fmt.Errorf("sys: kill: refusing to signal pid %d", pid)
	}
	sig := syscall.SIGTERM
	if sigName != "" {
		s := signalByName(sigName)
		if s == 0 {
			return fmt.Errorf("sys: kill: unknown signal %q", sigName)
		}
		sig = s
	}
	return syscall.Kill(int(pid), sig)
}

// signalByName converts a signal name string to a syscall.Signal.
// Accepts names like "SIGKILL", "KILL", "9".
func signalByName(name string) syscall.Signal {
	name = strings.ToUpper(strings.TrimPrefix(strings.ToUpper(name), "SIG"))
	switch name {
	case "HUP", "1":
		return syscall.SIGHUP
	case "INT", "2":
		return syscall.SIGINT
	case "QUIT", "3":
		return syscall.SIGQUIT
	case "ILL", "4":
		return syscall.SIGILL
	case "TRAP", "5":
		return syscall.SIGTRAP
	case "ABRT", "6":
		return syscall.SIGABRT
	case "KILL", "9":
		return syscall.SIGKILL
	case "USR1", "10":
		return syscall.SIGUSR1
	case "SEGV", "11":
		return syscall.SIGSEGV
	case "USR2", "12":
		return syscall.SIGUSR2
	case "PIPE", "13":
		return syscall.SIGPIPE
	case "ALRM", "14":
		return syscall.SIGALRM
	case "TERM", "15":
		return syscall.SIGTERM
	case "CONT", "18":
		return syscall.SIGCONT
	case "STOP", "19":
		return syscall.SIGSTOP
	case "TSTP", "20":
		return syscall.SIGTSTP
	case "CHLD", "17":
		return syscall.SIGCHLD
	default:
		// Try numeric.
		n, err := strconv.Atoi(name)
		if err == nil && n > 0 {
			return syscall.Signal(n)
		}
		return 0
	}
}
