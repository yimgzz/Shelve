package model

import (
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"

	"shelve/internal/sshx/args"
)

const (
	// MinPort / MaxPort are the allowed TCP port bounds (master plan §4).
	MinPort = 1
	MaxPort = 65535

	maxHostLen = 253
)

// ValidationError is a precise field-level validation failure
// (master plan §4: field name + rule).
type ValidationError struct {
	Field string
	Rule  string
}

func (e *ValidationError) Error() string {
	return e.Field + ": " + e.Rule
}

func fieldErr(field, format string, args ...any) error {
	return &ValidationError{Field: field, Rule: fmt.Sprintf(format, args...)}
}

func joinErrs(errs ...error) error {
	var out []error
	for _, e := range errs {
		if e != nil {
			out = append(out, e)
		}
	}
	switch len(out) {
	case 0:
		return nil
	case 1:
		return out[0]
	default:
		return errors.Join(out...)
	}
}

// hostLabel matches one RFC 1123 label: alphanumerics plus hyphens,
// must start and end with an alphanumeric, up to 63 chars.
var hostLabelRe = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$`)

// validHost reports whether host is a valid hostname, IPv4 or IPv6
// literal (master plan §4: host regex).
func validHost(host string) bool {
	if host == "" {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return true // IPv4 or IPv6 literal
	}
	if len(host) > maxHostLen {
		return false
	}
	labels := strings.Split(host, ".")
	allNumeric := true
	for _, l := range labels {
		if !hostLabelRe.MatchString(l) {
			return false
		}
		if _, err := strconv.Atoi(l); err != nil {
			allNumeric = false
		}
	}
	// A dotted all-numeric name that is not a valid IPv4 address
	// (e.g. "1.2.3", "1.2.3.4.5") is rejected, not treated as a
	// hostname — it reads as a botched IP.
	if allNumeric && strings.Contains(host, ".") {
		return false
	}
	return true
}

// isIPv6Literal reports whether host is an IPv6 address literal
// (plan P009: IPv6 targets cannot be embedded in a bastion username).
func isIPv6Literal(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.To4() == nil
}

func validPort(port int) bool {
	return port >= MinPort && port <= MaxPort
}

func validNonEmpty(name string) bool {
	return strings.TrimSpace(name) != ""
}

// validSftpPath accepts an empty value (use the global default), "~" or a
// "~/"-relative path, or an absolute path. Relative/non-absolute paths other
// than "~" are rejected (plan P002 §4.1).
func validSftpPath(p string) bool {
	switch {
	case p == "":
		return true
	case p == "~" || strings.HasPrefix(p, "~/"):
		return true
	default:
		return strings.HasPrefix(p, "/")
	}
}

// checkAuth enforces the auth rules for one Auth (session or jump host):
// exactly one of password/keyPath must be set, and Type must agree with
// which one is set (master plan §4 XOR rule).
func checkAuth(prefix string, a Auth) []error {
	var errs []error
	if a.Password != "" && a.KeyPath != "" {
		errs = append(errs, fieldErr(prefix+".auth", "exactly one of password or keyPath may be set"))
	} else if a.Password == "" && a.KeyPath == "" {
		errs = append(errs, fieldErr(prefix+".auth", "exactly one of password or keyPath must be set"))
	}
	switch a.Type {
	case AuthPassword:
		if a.Password == "" && a.KeyPath != "" {
			errs = append(errs, fieldErr(prefix+".auth", "password is required when authType is password, not keyPath"))
		}
	case AuthKey:
		if a.KeyPath == "" && a.Password != "" {
			errs = append(errs, fieldErr(prefix+".auth", "keyPath is required when authType is key, not password"))
		}
	default:
		errs = append(errs, fieldErr(prefix+".auth", "authType must be password or key"))
	}
	return errs
}

// checkAuthOptional is checkAuth with the "exactly one must be set" rule
// relaxed to "may be empty; if set, must still be a consistent XOR"
// (plan P009 §2.5): when a bastion hop is present, the session (target)
// credential is unused at connect time because the bastion relay handles
// target authentication. A fully-empty Auth is therefore valid.
func checkAuthOptional(prefix string, a Auth) []error {
	var errs []error
	if a.Password != "" && a.KeyPath != "" {
		errs = append(errs, fieldErr(prefix+".auth", "exactly one of password or keyPath may be set"))
	}
	switch a.Type {
	case AuthPassword:
		if a.Password == "" && a.KeyPath != "" {
			errs = append(errs, fieldErr(prefix+".auth", "password is required when authType is password, not keyPath"))
		}
	case AuthKey:
		if a.KeyPath == "" && a.Password != "" {
			errs = append(errs, fieldErr(prefix+".auth", "keyPath is required when authType is key, not password"))
		}
	default:
		errs = append(errs, fieldErr(prefix+".auth", "authType must be password or key"))
	}
	return errs
}

// checkBastionUser enforces the plan P009 rule that a bastion hop's user
// must not contain '@': the target is embedded in the SSH username as
// user@target, so the user part must stay unambiguous.
func checkBastionUser(prefix, user string) error {
	if strings.Contains(user, "@") {
		return fieldErr(prefix+".user", "bastion user must not contain '@' (the target is embedded in the username)")
	}
	return nil
}

// bastionIndexes returns the indexes of the bastion-marked hops in an
// ordered jump chain (plan P009). Empty when no hop is marked.
func bastionIndexes(jumps []JumpHost) []int {
	var out []int
	for i, j := range jumps {
		if j.Bastion {
			out = append(out, i)
		}
	}
	return out
}

// Validate checks Folder invariants (master plan §4).
func (f *Folder) Validate() error {
	if !validNonEmpty(f.Name) {
		return fieldErr("folder.name", "must not be empty")
	}
	return nil
}

// Validate checks Credential invariants (plan P003 §4.1): a display name,
// a non-empty login, and exactly one auth method (password XOR key path —
// the same rule as sessions/jump hosts).
func (c *Credential) Validate() error {
	var errs []error
	if !validNonEmpty(c.Name) {
		errs = append(errs, fieldErr("credential.name", "must not be empty"))
	}
	if !validNonEmpty(c.User) {
		errs = append(errs, fieldErr("credential.user", "must not be empty"))
	}
	errs = append(errs, checkAuth("credential", c.Auth)...)
	return joinErrs(errs...)
}

// Validate checks SavedJumpHost invariants (plan P006): a display name,
// host, port, login, and exactly one auth method (password XOR key path —
// the same rule as sessions/jump hosts).
func (s *SavedJumpHost) Validate() error {
	errs := s.validateFields("savedJumpHost")
	return joinErrs(errs...)
}

func (s *SavedJumpHost) validateFields(prefix string) []error {
	var errs []error
	if !validNonEmpty(s.Name) {
		errs = append(errs, fieldErr(prefix+".name", "must not be empty"))
	}
	if !validHost(s.Host) {
		errs = append(errs, fieldErr(prefix+".host", "must be a valid hostname, IPv4 or IPv6 address"))
	}
	if !validPort(s.Port) {
		errs = append(errs, fieldErr(prefix+".port", "must be in range %d-%d", MinPort, MaxPort))
	}
	if !validNonEmpty(s.User) {
		errs = append(errs, fieldErr(prefix+".user", "must not be empty"))
	}
	if s.Bastion {
		if err := checkBastionUser(prefix, s.User); err != nil {
			errs = append(errs, err)
		}
	}
	errs = append(errs, checkAuth(prefix, s.Auth)...)
	return errs
}

// Validate checks Session invariants (master plan §4): name, host, port,
// user, auth XOR, each jump host, and ExtraArgs (strict parser, §2 D5).
// All violations are joined into one error. The optional CredentialID
// reference is resolved by the store/service layer (an ID alone cannot
// be checked without vault state); see plan P003 §4.1.
func (s *Session) Validate() error {
	return joinErrs(s.validateFields("session")...)
}

func (s *Session) validateFields(prefix string) []error {
	var errs []error
	if !validNonEmpty(s.Name) {
		errs = append(errs, fieldErr(prefix+".name", "must not be empty"))
	}
	if !validHost(s.Host) {
		errs = append(errs, fieldErr(prefix+".host", "must be a valid hostname, IPv4 or IPv6 address"))
	}
	if !validPort(s.Port) {
		errs = append(errs, fieldErr(prefix+".port", "must be in range %d-%d", MinPort, MaxPort))
	}
	if !validNonEmpty(s.User) {
		errs = append(errs, fieldErr(prefix+".user", "must not be empty"))
	}
	if !validSftpPath(s.SftpInitialPath) {
		errs = append(errs, fieldErr(prefix+".sftpInitialPath", "must be empty, absolute, or ~-prefixed"))
	}

	// Bastion jump-host mode (plan P009): target credentials become
	// optional and a set of cross-hop rules apply.
	bastionIdxs := bastionIndexes(s.JumpHosts)
	hasBastion := len(bastionIdxs) > 0

	// With a bastion hop, the session (target) auth may be empty; when
	// set it must still be a consistent XOR (plan P009 §2.5).
	if hasBastion {
		errs = append(errs, checkAuthOptional(prefix, s.Auth)...)
	} else {
		errs = append(errs, checkAuth(prefix, s.Auth)...)
	}

	for i, j := range s.JumpHosts {
		errs = append(errs, j.validateFields(fmt.Sprintf("%s.jumpHosts[%d]", prefix, i))...)
	}

	if hasBastion {
		errs = append(errs, s.validateBastion(prefix, bastionIdxs)...)
	}

	if err := args.Validate(s.ExtraArgs); err != nil {
		errs = append(errs, fieldErr(prefix+".extraArgs", "%s", err))
	}
	return errs
}

// validateBastion enforces the session-level bastion rules (plan P009 §3):
// at most one bastion hop, and when present it must be the last hop; the
// target must not be IPv6, must use port 22 (v1), and Extra Args must not
// add a ProxyJump (which would place another handshake after the bastion).
func (s *Session) validateBastion(prefix string, bastionIdxs []int) []error {
	var errs []error
	last := len(s.JumpHosts) - 1

	if len(bastionIdxs) > 1 {
		// More than one bastion: flag every bastion hop.
		for _, i := range bastionIdxs {
			errs = append(errs, fieldErr(fmt.Sprintf("%s.jumpHosts[%d].bastion", prefix, i),
				"at most one bastion jump host is allowed"))
		}
	} else if bastionIdxs[0] != last {
		// A single bastion that is not the final hop.
		errs = append(errs, fieldErr(fmt.Sprintf("%s.jumpHosts[%d].bastion", prefix, bastionIdxs[0]),
			"the bastion jump host must be the last hop"))
	}

	if s.Port != 22 {
		errs = append(errs, fieldErr(prefix+".port", "bastion target requires port 22 (v1 limitation)"))
	}
	if isIPv6Literal(s.Host) {
		errs = append(errs, fieldErr(prefix+".host", "bastion target requires a hostname or IPv4 (no IPv6 in bastion mode)"))
	}
	if p, perr := args.Parse(s.ExtraArgs); perr == nil && p.ProxyJump != nil {
		errs = append(errs, fieldErr(prefix+".extraArgs", "a bastion jump host cannot be combined with ProxyJump"))
	}
	return errs
}

// Validate checks JumpHost invariants (master plan §4).
func (j *JumpHost) Validate() error {
	errs := j.validateFields("jumpHost")
	return joinErrs(errs...)
}

func (j *JumpHost) validateFields(prefix string) []error {
	var errs []error
	if !validHost(j.Host) {
		errs = append(errs, fieldErr(prefix+".host", "must be a valid hostname, IPv4 or IPv6 address"))
	}
	if !validPort(j.Port) {
		errs = append(errs, fieldErr(prefix+".port", "must be in range %d-%d", MinPort, MaxPort))
	}
	if !validNonEmpty(j.User) {
		errs = append(errs, fieldErr(prefix+".user", "must not be empty"))
	}
	if j.Bastion {
		if err := checkBastionUser(prefix, j.User); err != nil {
			errs = append(errs, err)
		}
	}
	errs = append(errs, checkAuth(prefix, j.Auth)...)
	return errs
}
