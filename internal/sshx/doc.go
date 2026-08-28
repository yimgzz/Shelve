// Package sshx provides SSH helpers independent of the live engine:
// the "Extra Args" parser (-L/-D/-o curated subset, ProxyJump), the
// app-managed known_hosts file (TOFU), the host-key callback factory
// and SSH auth-method builders (master plan §2 D5, A1/A2).
//
// Populated in Phase 3.
package sshx
