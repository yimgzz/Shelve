// Package sshx provides SSH helpers independent of the live engine:
// the "Extra Args" parser (-L/-D/-o curated subset, ProxyJump;
// master plan §2 D5), the app-managed known_hosts file (TOFU, A1), the
// host-key callback factory and SSH auth-method builders (Phase 3).
//
// Phase 2 lands the known_hosts implementation and the Extra Args
// placeholder; the strict parser replaces it in Phase 3.
package sshx

import "errors"

// ErrExtraArgsNotWired is returned by the Phase 2 ValidateExtraArgs
// placeholder for any non-empty string. The strict parser (master plan
// §2 D5: -L/-D, curated -o subset, ProxyJump) lands in Phase 3.
var ErrExtraArgsNotWired = errors.New("sshx: extra args parser not wired yet (Phase 3)")

// ValidateExtraArgs validates the session "Extra Args" string.
//
// Phase 2 placeholder: only the empty string is accepted; the strict
// parser arrives with the engine in Phase 3.
func ValidateExtraArgs(s string) error {
	if s == "" {
		return nil
	}
	return ErrExtraArgsNotWired
}
