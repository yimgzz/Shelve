// Package termws implements the plan P005 terminal I/O transport: a
// same-origin WebSocket carrying raw terminal bytes between the engine and
// the webview, bypassing the Wails v3 event bridge — which starves keyboard
// input (promise-chain microtask starvation) and churns goroutines/threads
// under sustained output floods (pthread_create EAGAIN).
//
// The socket lives on the app's existing HTTP transport (the asset handler
// is wrapped in main.go), so the webview connects with
// ws(s)://<location.host>/terminal and no separate port or credential is
// needed. Exactly one connection is active (single-window app, master plan
// A7); a second upgrade is rejected.
//
// Security (master plan §8.9, documented exception): the listener is the
// app's own loopback-bound HTTP transport; Origin is validated against the
// known local origins; frames are capped.
package termws

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Frame format (both directions, one frame per WebSocket binary message):
//
//	+--------+------------------+----------+----------------+
//	| u8 len | tabID ASCII      | u32 size | payload bytes  |
//	+--------+------------------+----------+----------------+
//
// tabID is the tab's ULID string. Payloads above maxPayload are split into
// several frames by the sender.

const (
	// maxTabIDLen bounds the tabID field (ULIDs are 26 ASCII chars).
	maxTabIDLen = 64
	// maxPayload caps one frame's payload; larger pump batches are split.
	maxPayload = 256 * 1024
)

// AppendFrame appends one encoded frame to dst and returns the result.
// Errors are programming bugs (over-long tabID / oversize payload) and are
// never caused by remote data.
func AppendFrame(dst []byte, tabID string, payload []byte) ([]byte, error) {
	if len(tabID) == 0 || len(tabID) > maxTabIDLen {
		return dst, fmt.Errorf("termws: invalid tabID length %d", len(tabID))
	}
	if len(payload) > maxPayload {
		return dst, fmt.Errorf("termws: payload %d exceeds cap %d", len(payload), maxPayload)
	}
	dst = append(dst, byte(len(tabID)))
	dst = append(dst, tabID...)
	dst = binary.BigEndian.AppendUint32(dst, uint32(len(payload)))
	return append(dst, payload...), nil
}

// DecodeFrame parses one complete frame (a single WebSocket message).
func DecodeFrame(b []byte) (tabID string, payload []byte, err error) {
	if len(b) < 5 {
		return "", nil, errors.New("termws: frame too short")
	}
	tabLen := int(b[0])
	if tabLen == 0 || tabLen > maxTabIDLen {
		return "", nil, fmt.Errorf("termws: bad tabID length %d", tabLen)
	}
	if len(b) < 1+tabLen+4 {
		return "", nil, errors.New("termws: truncated tabID")
	}
	plen := binary.BigEndian.Uint32(b[1+tabLen:])
	if plen > maxPayload {
		return "", nil, fmt.Errorf("termws: payload %d exceeds cap %d", plen, maxPayload)
	}
	off := 1 + tabLen + 4
	if uint64(len(b)) < uint64(off)+uint64(plen) {
		return "", nil, errors.New("termws: truncated payload")
	}
	return string(b[1 : 1+tabLen]), b[off : off+int(plen)], nil
}
