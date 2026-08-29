// Package sshx provides SSH helpers independent of the live engine:
// the "Extra Args" parser (-L/-D/-o curated subset, ProxyJump), the
// app-managed known_hosts file (TOFU), the host-key callback factory
// and SSH auth-method builders (master plan §2 D5, A1/A2).
//
// Phase 3a added the Extra Args parser (args) and known_hosts (knownhosts);
// Phase 3b adds AuthMethods and NewHostKeyCallback, the two entry points
// the 3c sshengine consumes.
package sshx
