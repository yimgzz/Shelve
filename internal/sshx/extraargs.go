package sshx

import "dummy-ssh-manager/internal/sshx/args"

// ValidateExtraArgs validates the session "Extra Args" string with the
// strict parser (master plan §2 D5: -L/-D, curated -o subset,
// ProxyJump). Error messages are user-facing and shown verbatim in the
// session editor's inline validation.
func ValidateExtraArgs(s string) error {
	return args.Validate(s)
}
