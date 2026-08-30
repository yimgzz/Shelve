// Package sshengine is the SSH session engine (master plan §5): the
// dial chain (structured jump hosts + Extra Args ProxyJump), the PTY
// session, batched event emission (≤50 ms / ≤16 KB, master plan A6),
// the host-key and key-passphrase prompt flows, window resizing,
// disconnect/reconnect and shutdown. One run goroutine per open
// connection plus its read pump. Local port forwards (ssh:forward) and
// the in-process dial used by TestConnection land in Phase 3d.
package sshengine
