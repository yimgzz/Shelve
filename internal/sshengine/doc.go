// Package sshengine is the SessionManager: dialing (jump-host chains),
// PTY sessions, batched event emission (≤50 ms / ≤16 KB, master plan A6),
// window resizing, local port forwards, disconnect/reconnect and the
// key-passphrase prompt flow. One goroutine per open connection.
//
// Populated in Phase 3.
package sshengine
