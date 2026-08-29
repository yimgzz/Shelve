// Package args is the strict tokenizer + parser for the session
// "Extra Args" string (master plan §2 D5).
//
// Accepted grammar — any number of the following tokens, separated by
// whitespace. Shell-style quoting: a single or double quote opens a
// quoted region that only the same quote character closes (unbalanced
// quotes are an error) and backslashes have no escape meaning (a
// backslash is a literal character). Quoted whitespace does not join
// tokens — the grammar has no multi-word values — so wrapping a whole
// spec in quotes (e.g. when pasting from shell docs) still works:
//
//	"-L 127.0.0.1:8080:db:5432"   is the same as  -L 127.0.0.1:8080:db:5432
//
//	-L [bind:]localPort:dstHost:dstPort
//	-D [bind:]localPort
//	-o ServerAliveInterval=<int>
//	-o ServerAliveCountMax=<int>
//	-o ConnectTimeout=<int>
//	-o StrictHostKeyChecking=no|ask
//	ProxyJump=[user@]host[:port]
//
// Normalization and rules:
//   - An omitted bind address is normalized to 127.0.0.1 in the
//     Parsed output (forwards listen on the loopback by default,
//     master plan §8.9).
//   - An omitted ProxyJump port is normalized to 0, meaning the SSH
//     default (22).
//   - Hosts (bind address, destination host, ProxyJump host) must be
//     hostnames or IPv4 literals. IPv6 addresses are a documented v1
//     limitation and are rejected with a specific error.
//   - All explicit ports must be in 1-65535.
//   - Duplicate -o keys: the last occurrence wins.
//   - At most one ProxyJump per spec; for jump chains use the session
//     card's structured Jump Hosts instead.
//
// Error messages are user-facing: the session editor shows them
// verbatim in its inline Extra Args validation (master plan §6).
package args

import (
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
)

const (
	minPort = 1
	maxPort = 65535

	defaultBind = "127.0.0.1"
	maxHostLen  = 253
)

var (
	// ErrUnbalancedQuote is returned by the tokenizer when a quote is
	// not closed.
	ErrUnbalancedQuote = errors.New("unbalanced quote")

	errIPv6 = errors.New("IPv6 addresses are not supported in Extra Args (v1 limitation)")
)

// Forward is one local port forward parsed from -L (Kind "L") or -D
// (Kind "D"). For Kind "D", DstHost and DstPort are zero-valued.
// BindAddr is normalized: an omitted bind becomes "127.0.0.1".
type Forward struct {
	Kind      string // "L" or "D"
	BindAddr  string
	LocalPort int
	DstHost   string
	DstPort   int
}

// Options is the curated -o subset (master plan §2 D5). The pointer
// fields are non-nil only when the option is present in the spec; for
// duplicate keys the last occurrence wins.
type Options struct {
	ServerAliveInterval   *int
	ServerAliveCountMax   *int
	ConnectTimeout        *int
	StrictHostKeyChecking string
}

// ProxyJump is the ProxyJump= shorthand for one jump host. Port 0
// means the SSH default (22).
type ProxyJump struct {
	User string
	Host string
	Port int // 0 = 22
}

// Parsed is the normalized view of an Extra Args spec. An empty or
// whitespace-only spec yields an empty Parsed.
type Parsed struct {
	Forwards  []Forward
	Options   Options
	ProxyJump *ProxyJump
}

// Parse tokenizes spec and interprets every token (see the package
// doc for the accepted grammar and normalization rules).
func Parse(spec string) (*Parsed, error) {
	tokens, err := tokenize(spec)
	if err != nil {
		return nil, err
	}
	p := &Parsed{}
	for i := 0; i < len(tokens); i++ {
		tok := tokens[i]
		switch {
		case tok == "-L":
			val, ok := nextValue(tokens, &i)
			if !ok {
				return nil, errors.New("missing value for -L")
			}
			f, err := parseLocalForward(val)
			if err != nil {
				return nil, err
			}
			p.Forwards = append(p.Forwards, f)
		case tok == "-D":
			val, ok := nextValue(tokens, &i)
			if !ok {
				return nil, errors.New("missing value for -D")
			}
			f, err := parseDynamicForward(val)
			if err != nil {
				return nil, err
			}
			p.Forwards = append(p.Forwards, f)
		case tok == "-o":
			val, ok := nextValue(tokens, &i)
			if !ok {
				return nil, errors.New("missing value for -o")
			}
			if err := applyOption(&p.Options, val); err != nil {
				return nil, err
			}
		case strings.HasPrefix(tok, "ProxyJump="):
			if p.ProxyJump != nil {
				return nil, errors.New("duplicate ProxyJump (only one is allowed)")
			}
			pj, err := parseProxyJump(tok[len("ProxyJump="):])
			if err != nil {
				return nil, err
			}
			p.ProxyJump = pj
		case len(tok) > 1 && tok[0] == '-':
			return nil, fmt.Errorf("unsupported flag %s", tok)
		default:
			return nil, fmt.Errorf("bare word %q is not supported", tok)
		}
	}
	return p, nil
}

// Validate parses spec and re-checks the normalized port ranges. It is
// the UI validation entry point (see sshx.ValidateExtraArgs): valid
// specs return nil, invalid ones return the parser's user-facing error.
func Validate(spec string) error {
	p, err := Parse(spec)
	if err != nil {
		return err
	}
	for _, f := range p.Forwards {
		if !inPortRange(f.LocalPort) {
			return fmt.Errorf("invalid local port %d (want %d-%d)", f.LocalPort, minPort, maxPort)
		}
		if f.Kind == "L" && !inPortRange(f.DstPort) {
			return fmt.Errorf("invalid destination port %d (want %d-%d)", f.DstPort, minPort, maxPort)
		}
	}
	if pj := p.ProxyJump; pj != nil && pj.Port != 0 && !inPortRange(pj.Port) {
		return fmt.Errorf("invalid ProxyJump port %d (want %d-%d)", pj.Port, minPort, maxPort)
	}
	return nil
}

// tokenize strips quotes and splits the result on whitespace into
// tokens (see the package doc for the quoting rules).
func tokenize(spec string) ([]string, error) {
	var stripped strings.Builder
	stripped.Grow(len(spec))
	quote := byte(0)
	for i := 0; i < len(spec); i++ {
		c := spec[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			} else {
				stripped.WriteByte(c)
			}
		case c == '\'' || c == '"':
			quote = c
		default:
			stripped.WriteByte(c)
		}
	}
	if quote != 0 {
		return nil, ErrUnbalancedQuote
	}
	return strings.FieldsFunc(stripped.String(), isSpace), nil
}

func isSpace(c rune) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

// nextValue consumes the token following a flag and returns it.
func nextValue(tokens []string, i *int) (string, bool) {
	if *i+1 >= len(tokens) {
		return "", false
	}
	*i++
	return tokens[*i], true
}

// checkIPv6 rejects the documented v1 limitation: bracketed IPv6
// addresses (the form IPv6 takes in -L/-D/ProxyJump host positions).
// Unbracketed consecutive colons are left to the normal part-count and
// empty-part errors so typos like "8080::5432" get the actionable
// "empty destination host" message.
func checkIPv6(val string) error {
	if strings.ContainsRune(val, '[') || strings.ContainsRune(val, ']') {
		return errIPv6
	}
	return nil
}

// checkBind validates an explicit bind address; it must be a hostname
// or IPv4 literal.
func checkBind(bind, flag, val string) error {
	if bind == "" {
		return fmt.Errorf("empty bind address in %s spec: %q", flag, val)
	}
	if !isHostOrIPv4(bind) {
		return fmt.Errorf("invalid bind address in %s spec: %q", flag, val)
	}
	return nil
}

func parseLocalForward(val string) (Forward, error) {
	if err := checkIPv6(val); err != nil {
		return Forward{}, err
	}
	parts := strings.Split(val, ":")
	f := Forward{Kind: "L", BindAddr: defaultBind}
	switch len(parts) {
	case 4:
		if err := checkBind(parts[0], "-L", val); err != nil {
			return Forward{}, err
		}
		f.BindAddr = parts[0]
		parts = parts[1:]
	case 3:
	default:
		return Forward{}, fmt.Errorf("invalid -L spec: %q (expected [bind:]localPort:dstHost:dstPort)", val)
	}
	lp, err := parsePort(parts[0])
	if err != nil {
		return Forward{}, fmt.Errorf("invalid local port in -L spec: %q", val)
	}
	f.LocalPort = lp
	if parts[1] == "" {
		return Forward{}, fmt.Errorf("empty destination host in -L spec: %q", val)
	}
	if !isHostOrIPv4(parts[1]) {
		return Forward{}, fmt.Errorf("invalid destination host in -L spec: %q", val)
	}
	f.DstHost = parts[1]
	dp, err := parsePort(parts[2])
	if err != nil {
		return Forward{}, fmt.Errorf("invalid destination port in -L spec: %q", val)
	}
	f.DstPort = dp
	return f, nil
}

func parseDynamicForward(val string) (Forward, error) {
	if err := checkIPv6(val); err != nil {
		return Forward{}, err
	}
	parts := strings.Split(val, ":")
	f := Forward{Kind: "D", BindAddr: defaultBind}
	switch len(parts) {
	case 2:
		if err := checkBind(parts[0], "-D", val); err != nil {
			return Forward{}, err
		}
		f.BindAddr = parts[0]
		parts = parts[1:]
	case 1:
	default:
		return Forward{}, fmt.Errorf("invalid -D spec: %q (expected [bind:]localPort)", val)
	}
	lp, err := parsePort(parts[0])
	if err != nil {
		return Forward{}, fmt.Errorf("invalid local port in -D spec: %q", val)
	}
	f.LocalPort = lp
	return f, nil
}

func parseProxyJump(val string) (*ProxyJump, error) {
	if err := checkIPv6(val); err != nil {
		return nil, err
	}
	invalid := func() error {
		return fmt.Errorf("invalid ProxyJump spec: %q (expected [user@]host[:port])", val)
	}
	var user, hostPort string
	if at := strings.Index(val, "@"); at >= 0 {
		user = val[:at]
		if user == "" || strings.Contains(val[at+1:], "@") {
			return nil, invalid()
		}
		hostPort = val[at+1:]
	} else {
		hostPort = val
	}
	pj := &ProxyJump{User: user}
	if idx := strings.LastIndex(hostPort, ":"); idx >= 0 {
		port, err := parsePort(hostPort[idx+1:])
		if err != nil {
			return nil, invalid()
		}
		pj.Port = port
		hostPort = hostPort[:idx]
	}
	if !isHostOrIPv4(hostPort) {
		return nil, invalid()
	}
	pj.Host = hostPort
	return pj, nil
}

func applyOption(opts *Options, val string) error {
	eq := strings.Index(val, "=")
	if eq < 0 {
		return fmt.Errorf("invalid -o value: %q (expected Key=Value)", val)
	}
	key := val[:eq]
	if key == "" {
		return fmt.Errorf("invalid -o value: %q (expected Key=Value)", val)
	}
	v := val[eq+1:]
	switch key {
	case "ServerAliveInterval", "ServerAliveCountMax", "ConnectTimeout":
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return fmt.Errorf("invalid value for -o %s: %q (expected a non-negative integer)", key, v)
		}
		switch key {
		case "ServerAliveInterval":
			opts.ServerAliveInterval = &n
		case "ServerAliveCountMax":
			opts.ServerAliveCountMax = &n
		}
		// default: ConnectTimeout
		if key == "ConnectTimeout" {
			opts.ConnectTimeout = &n
		}
		return nil
	case "StrictHostKeyChecking":
		if v != "no" && v != "ask" {
			return fmt.Errorf("invalid value for -o StrictHostKeyChecking: %q (expected \"no\" or \"ask\")", v)
		}
		opts.StrictHostKeyChecking = v
		return nil
	default:
		return fmt.Errorf("unsupported -o key %q", key)
	}
}

// parsePort parses a TCP port: all digits, in 1-65535.
func parsePort(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || !inPortRange(n) {
		return 0, errors.New("out of range")
	}
	return n, nil
}

func inPortRange(n int) bool {
	return n >= minPort && n <= maxPort
}

var hostLabelRe = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$`)

// isHostOrIPv4 reports whether s is a valid hostname or an IPv4
// literal. IPv6 is intentionally unsupported (v1 limitation), and a
// dotted all-numeric name that is not a valid IPv4 address (e.g.
// "1.2.3") is rejected as a botched IP, consistent with model host
// validation (master plan §4).
func isHostOrIPv4(s string) bool {
	if s == "" || len(s) > maxHostLen {
		return false
	}
	if ip := net.ParseIP(s); ip != nil {
		return ip.To4() != nil
	}
	labels := strings.Split(s, ".")
	allNumeric := true
	for _, l := range labels {
		if !hostLabelRe.MatchString(l) {
			return false
		}
		if _, err := strconv.Atoi(l); err != nil {
			allNumeric = false
		}
	}
	return !(allNumeric && strings.Contains(s, "."))
}
