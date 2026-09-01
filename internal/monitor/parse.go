package monitor

import (
	"bufio"
	"bytes"
	"fmt"
	"strconv"
	"strings"
)

// rawSample is one parsed collectScript output. CP holds the /proc/stat cpu
// aggregate counters in order: user nice system idle iowait irq softirq
// steal. Memory values are KiB; NET values are cumulative bytes; DiskTotal/
// DiskUsed are KiB from `df -kP`.
type rawSample struct {
	Hostname    string
	CP          [8]uint64
	MemTotal    uint64 // KiB
	MemAvail    uint64 // KiB
	NetRx       uint64 // cumulative rx bytes (all non-loopback ifaces)
	NetTx       uint64 // cumulative tx bytes (all non-loopback ifaces)
	UptimeSec   uint64
	DiskTotal   uint64  // KiB, main partition (/)
	DiskUsed    uint64  // KiB, main partition (/)
	DiskUsedPct float64 // e.g. 42.0 for "42%"
	DiskRoot    string  // mount point of the main partition
	DfText      string  // full `df -h` output (verbatim, DFH block)
}

// parseSample parses the tagged output of collectScript. Individual lines
// that are absent or malformed leave their field zero (partial samples are
// tolerated — plan P004 D5); a completely empty/unrecognizable output is an
// error so the ticker treats it as a failed sample.
func parseSample(out []byte) (*rawSample, error) {
	s := &rawSample{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	inDf := false
	var dfBuf strings.Builder
	lines := 0
	for sc.Scan() {
		lines++
		line := sc.Text()
		if inDf {
			if line == "DFH_END" {
				inDf = false
				continue
			}
			dfBuf.WriteString(line)
			dfBuf.WriteByte('\n')
			continue
		}
		switch {
		case strings.HasPrefix(line, "DFH_START"):
			inDf = true
		case strings.HasPrefix(line, "HN="):
			s.Hostname = strings.TrimSpace(strings.TrimPrefix(line, "HN="))
		case strings.HasPrefix(line, "CP="):
			fields := strings.Fields(strings.TrimPrefix(line, "CP="))
			for i := 0; i < 8 && i < len(fields); i++ {
				v, err := strconv.ParseUint(fields[i], 10, 64)
				if err != nil {
					return nil, fmt.Errorf("monitor: parse cpu field %d: %w", i, err)
				}
				s.CP[i] = v
			}
		case strings.HasPrefix(line, "MEM="):
			parts := strings.Fields(strings.TrimPrefix(line, "MEM="))
			if len(parts) >= 2 {
				t, e1 := strconv.ParseUint(parts[0], 10, 64)
				a, e2 := strconv.ParseUint(parts[1], 10, 64)
				if e1 != nil || e2 != nil {
					return nil, fmt.Errorf("monitor: parse mem: %q", line)
				}
				s.MemTotal, s.MemAvail = t, a
			}
		case strings.HasPrefix(line, "NET="):
			parts := strings.Fields(strings.TrimPrefix(line, "NET="))
			if len(parts) >= 2 {
				rx, e1 := strconv.ParseUint(parts[0], 10, 64)
				tx, e2 := strconv.ParseUint(parts[1], 10, 64)
				if e1 != nil || e2 != nil {
					return nil, fmt.Errorf("monitor: parse net: %q", line)
				}
				s.NetRx, s.NetTx = rx, tx
			}
		case strings.HasPrefix(line, "UP="):
			if v, err := strconv.ParseUint(strings.TrimSpace(strings.TrimPrefix(line, "UP=")), 10, 64); err == nil {
				s.UptimeSec = v
			}
		case strings.HasPrefix(line, "DFROOT="):
			parts := strings.Fields(strings.TrimPrefix(line, "DFROOT="))
			if len(parts) >= 5 {
				if t, err := strconv.ParseUint(parts[0], 10, 64); err == nil {
					s.DiskTotal = t
				}
				if u, err := strconv.ParseUint(parts[1], 10, 64); err == nil {
					s.DiskUsed = u
				}
				if p, err := strconv.ParseFloat(strings.TrimSuffix(parts[3], "%"), 64); err == nil {
					s.DiskUsedPct = p
				}
				s.DiskRoot = parts[4]
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if lines == 0 {
		return nil, fmt.Errorf("monitor: empty script output")
	}
	s.DfText = dfBuf.String()
	return s, nil
}
