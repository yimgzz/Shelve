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
	errs = append(errs, checkAuth(prefix, s.Auth)...)
	for i, j := range s.JumpHosts {
		errs = append(errs, j.validateFields(fmt.Sprintf("%s.jumpHosts[%d]", prefix, i))...)
	}
	if err := args.Validate(s.ExtraArgs); err != nil {
		errs = append(errs, fieldErr(prefix+".extraArgs", "%s", err))
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
	errs = append(errs, checkAuth(prefix, j.Auth)...)
	return errs
}
