//go:build !darwin && !linux && !freebsd && !openbsd && !netbsd

package sys

import "fmt"

// topProcesses returns nil on unsupported platforms.
func topProcesses(n int) []procStat {
	return nil
}

// killProcess is a no-op stub on unsupported platforms.
func killProcess(pid int32, sigName string) error {
	return fmt.Errorf("sys: kill: not supported on this platform")
}
