package monitor

import (
	"context"
	"fmt"

	"golang.org/x/crypto/ssh"
)

// collectScript reads all monitor metrics in ONE remote exec per tick
// (plan P004 D1): hostname, /proc/stat cpu counters, /proc/meminfo,
// /proc/net/dev cumulative byte counters (loopback excluded), uptime, the
// main partition's df -kP row, and the full human-readable `df -h` listing
// for the hover tooltip (plan P004 D6).
//
// The script is a STATIC constant — no user input is ever interpolated, so
// there is no injection surface (master plan §8.3). It only runs read-only
// commands (cat/awk/df) as the SSH user, at the same privilege level as the
// terminal itself (plan P004 D9). LC_ALL=C keeps df output stable across
// locales.
const collectScript = `export LC_ALL=C
printf 'HN=%s\n' "$(cat /proc/sys/kernel/hostname 2>/dev/null)"
awk '/^cpu /{printf "CP=%s %s %s %s %s %s %s\n", $2,$3,$4,$5,$6,$7,$8}' /proc/stat
awk '/^MemTotal:/{t=$2} /^MemAvailable:/{a=$2} /^MemFree:/{f=$2} /^Buffers:/{b=$2} /^Cached:/{c=$2} END{if(!a)a=f+b+c; printf "MEM=%d %d\n", t, a}' /proc/meminfo
awk 'NR>2 && $1 !~ /^lo:/ {rx+=$2; tx+=$10} END{printf "NET=%d %d\n", rx, tx}' /proc/net/dev
awk '{print "UP=" int($1)}' /proc/uptime
df -kP / | awk 'NR==2 {printf "DFROOT=%s %s %s %s %s\n", $2,$3,$4,$5,$6}'
printf 'DFH_START\n'
df -h
printf 'DFH_END\n'
`

// execScript runs the collection script on the client over a single exec
// channel and returns the combined output. A watchdog goroutine closes the
// session when ctx completes, so a stuck remote command can never hang the
// ticker past ExecTimeout.
func execScript(ctx context.Context, client *ssh.Client, script string) ([]byte, error) {
	sess, err := client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("monitor: new session: %w", err)
	}
	defer sess.Close()

	type result struct {
		out []byte
		err error
	}
	ch := make(chan result, 1)
	go func() {
		out, err := sess.CombinedOutput(script)
		ch <- result{out: out, err: err}
	}()

	select {
	case r := <-ch:
		return r.out, r.err
	case <-ctx.Done():
		// Closing the session unblocks CombinedOutput; the goroutine's
		// buffered send is consumed by the channel GC with the result.
		_ = sess.Close()
		return nil, ctx.Err()
	}
}
