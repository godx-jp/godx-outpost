//go:build !darwin && !linux && !freebsd && !openbsd && !netbsd

package sys

// cpuPercent returns 0 on unsupported platforms.
func cpuPercent() float64 {
	return 0
}
