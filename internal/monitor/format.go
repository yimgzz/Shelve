package monitor

import (
	"fmt"
	"time"
)

// delta returns b-a with saturating semantics on a counter wrap-around
// (a > b): after a reset/reboot the accumulated value "b since reset" is the
// closest usable estimate.
func delta(a, b uint64) uint64 {
	if b >= a {
		return b - a
	}
	return b
}

// cpuPercent is the CPU utilization between two /proc/stat cpu samples:
// 100 * busyΔ / totalΔ, clamped to [0,100]. Busy = user+nice+system+iowait+
// irq+softirq+steal (everything except idle). A zero total delta yields 0.
func cpuPercent(prev, cur *rawSample) float64 {
	busy := func(s *rawSample) uint64 {
		return s.CP[0] + s.CP[1] + s.CP[2] + s.CP[4] + s.CP[5] + s.CP[6] + s.CP[7]
	}
	busyΔ := delta(busy(prev), busy(cur))
	idleΔ := delta(prev.CP[3], cur.CP[3])
	totalΔ := busyΔ + idleΔ
	if totalΔ == 0 {
		return 0
	}
	if busyΔ > totalΔ {
		busyΔ = totalΔ
	}
	return 100 * float64(busyΔ) / float64(totalΔ)
}

// netSpeeds returns (up, down) bytes per second from two cumulative
// /proc/net/dev samples and the measured elapsed time. A non-positive
// elapsed or a counter reset yields 0 for the affected direction.
func netSpeeds(prev, cur *rawSample, elapsed time.Duration) (up, down uint64) {
	if elapsed <= 0 {
		return 0, 0
	}
	secs := float64(elapsed) / float64(time.Second)
	down = uint64(float64(delta(prev.NetRx, cur.NetRx)) / secs)
	up = uint64(float64(delta(prev.NetTx, cur.NetTx)) / secs)
	return up, down
}

// Binary byte suffixes for humanizeBytes (1024 base). Labels follow the plan
// P004 requirement wording: MB/GB/TB chosen dynamically; one decimal place.
const (
	mbBytes uint64 = 1 << 20
	gbBytes uint64 = 1 << 30
	tbBytes uint64 = 1 << 40
)

// humanizeBytes renders a byte count with MB/GB/TB labels chosen dynamically
// (plan P004 item 3): below 1 GiB → MB, below 1 TiB → GB, otherwise TB.
func humanizeBytes(b uint64) string {
	switch {
	case b >= tbBytes:
		return fmt.Sprintf("%.1f TB", float64(b)/float64(tbBytes))
	case b >= gbBytes:
		return fmt.Sprintf("%.1f GB", float64(b)/float64(gbBytes))
	default:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(mbBytes))
	}
}

// formatUptime renders seconds as `Xd Yh Zm`, below a day as `Yh Zm`, below
// an hour as `Ym`, below a minute as `Ys` (plan P004 item 6).
func formatUptime(seconds uint64) string {
	d := seconds / 86400
	h := (seconds % 86400) / 3600
	m := (seconds % 3600) / 60
	s := seconds % 60
	switch {
	case d > 0:
		return fmt.Sprintf("%dd %dh %dm", d, h, m)
	case h > 0:
		return fmt.Sprintf("%dh %dm", h, m)
	case m > 0:
		return fmt.Sprintf("%dm", m)
	default:
		return fmt.Sprintf("%ds", s)
	}
}
