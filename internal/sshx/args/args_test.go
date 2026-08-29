package args

import (
	"errors"
	"reflect"
	"testing"
)

func TestParseAccepted(t *testing.T) {
	i0, i5, i7, i10, i30 := 0, 5, 7, 10, 30
	tests := []struct {
		name string
		spec string
		want Parsed
	}{
		{"empty spec", "", Parsed{}},
		{"whitespace only", "  \t  \n", Parsed{}},
		{"-L bind omitted defaults to loopback", "-L 8080:db.example.com:5432", Parsed{
			Forwards: []Forward{{Kind: "L", BindAddr: "127.0.0.1", LocalPort: 8080, DstHost: "db.example.com", DstPort: 5432}},
		}},
		{"-L with explicit bind", "-L 192.168.1.50:8080:db.example.com:5432", Parsed{
			Forwards: []Forward{{Kind: "L", BindAddr: "192.168.1.50", LocalPort: 8080, DstHost: "db.example.com", DstPort: 5432}},
		}},
		{"-L hostname bind", "-L localhost:8080:db:5432", Parsed{
			Forwards: []Forward{{Kind: "L", BindAddr: "localhost", LocalPort: 8080, DstHost: "db", DstPort: 5432}},
		}},
		{"-L double-quoted spec", `"-L 127.0.0.1:8080:db:5432"`, Parsed{
			Forwards: []Forward{{Kind: "L", BindAddr: "127.0.0.1", LocalPort: 8080, DstHost: "db", DstPort: 5432}},
		}},
		{"-D bind omitted defaults to loopback", "-D 1080", Parsed{
			Forwards: []Forward{{Kind: "D", BindAddr: "127.0.0.1", LocalPort: 1080}},
		}},
		{"-D with explicit bind", "-D 0.0.0.0:1080", Parsed{
			Forwards: []Forward{{Kind: "D", BindAddr: "0.0.0.0", LocalPort: 1080}},
		}},
		{"-o ServerAliveInterval", "-o ServerAliveInterval=30", Parsed{
			Options: Options{ServerAliveInterval: &i30},
		}},
		{"-o ServerAliveInterval 0 allowed", "-o ServerAliveInterval=0", Parsed{
			Options: Options{ServerAliveInterval: &i0},
		}},
		{"-o ServerAliveCountMax", "-o ServerAliveCountMax=5", Parsed{
			Options: Options{ServerAliveCountMax: &i5},
		}},
		{"-o ServerAliveCountMax single-quoted", `'-o ServerAliveCountMax=7'`, Parsed{
			Options: Options{ServerAliveCountMax: &i7},
		}},
		{"-o ConnectTimeout", "-o ConnectTimeout=10", Parsed{
			Options: Options{ConnectTimeout: &i10},
		}},
		{"-o StrictHostKeyChecking no", "-o StrictHostKeyChecking=no", Parsed{
			Options: Options{StrictHostKeyChecking: "no"},
		}},
		{"-o StrictHostKeyChecking ask", "-o StrictHostKeyChecking=ask", Parsed{
			Options: Options{StrictHostKeyChecking: "ask"},
		}},
		{"ProxyJump user@host:port", "ProxyJump=bob@jump.example.com:2222", Parsed{
			ProxyJump: &ProxyJump{User: "bob", Host: "jump.example.com", Port: 2222},
		}},
		{"ProxyJump host:port", "ProxyJump=jump.example.com:2222", Parsed{
			ProxyJump: &ProxyJump{Host: "jump.example.com", Port: 2222},
		}},
		{"ProxyJump bare host port omitted", "ProxyJump=jump.example.com", Parsed{
			ProxyJump: &ProxyJump{Host: "jump.example.com", Port: 0},
		}},
		{"ProxyJump user@host no port", "ProxyJump=bob@jump", Parsed{
			ProxyJump: &ProxyJump{User: "bob", Host: "jump", Port: 0},
		}},
		{"duplicate -o last wins", "-o ServerAliveInterval=10 -o ServerAliveInterval=30", Parsed{
			Options: Options{ServerAliveInterval: &i30},
		}},
		{"multiple -L", "-L 8080:db:5432 -L 8443:web:80", Parsed{
			Forwards: []Forward{
				{Kind: "L", BindAddr: "127.0.0.1", LocalPort: 8080, DstHost: "db", DstPort: 5432},
				{Kind: "L", BindAddr: "127.0.0.1", LocalPort: 8443, DstHost: "web", DstPort: 80},
			},
		}},
		{"empty quoted token is a no-op", `"" -D 1080`, Parsed{
			Forwards: []Forward{{Kind: "D", BindAddr: "127.0.0.1", LocalPort: 1080}},
		}},
		{"combined", "-L 8080:db.example.com:5432 -D 1080 -o ConnectTimeout=10 -o ServerAliveInterval=30 -o StrictHostKeyChecking=ask ProxyJump=bob@jump.example.com:2222", Parsed{
			Forwards: []Forward{
				{Kind: "L", BindAddr: "127.0.0.1", LocalPort: 8080, DstHost: "db.example.com", DstPort: 5432},
				{Kind: "D", BindAddr: "127.0.0.1", LocalPort: 1080},
			},
			Options:   Options{ServerAliveInterval: &i30, ConnectTimeout: &i10, StrictHostKeyChecking: "ask"},
			ProxyJump: &ProxyJump{User: "bob", Host: "jump.example.com", Port: 2222},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.spec)
			if err != nil {
				t.Fatalf("Parse(%q) unexpected error: %v", tt.spec, err)
			}
			if !reflect.DeepEqual(*got, tt.want) {
				t.Fatalf("Parse(%q) = %+v, want %+v", tt.spec, *got, tt.want)
			}
		})
	}
}

func TestParseRejected(t *testing.T) {
	tests := []struct {
		name    string
		spec    string
		wantErr string
	}{
		// Unknown flags and bare words.
		{"unknown flag -A", "-A", "unsupported flag -A"},
		{"unknown flag -R with value", "-R 8080:db:5432", "unsupported flag -R"},
		{"unknown flag -C", "-C", "unsupported flag -C"},
		{"unknown flag among valid ones", "-L 8080:db:5432 -X", "unsupported flag -X"},
		{"flag-like case sensitivity", "-O ServerAliveInterval=30", "unsupported flag -O"},
		{"bare word", "foo", `bare word "foo" is not supported`},
		{"bare word after flag", "-L 8080:db:5432 extra", `bare word "extra" is not supported`},

		// Malformed -L.
		{"-L too few parts", "-L 8080:db", `invalid -L spec: "8080:db" (expected [bind:]localPort:dstHost:dstPort)`},
		{"-L too many parts", "-L 1:2:3:4:5", `invalid -L spec: "1:2:3:4:5" (expected [bind:]localPort:dstHost:dstPort)`},
		{"-L non-numeric local port", "-L 80x:db:5432", `invalid local port in -L spec: "80x:db:5432"`},
		{"-L local port 0", "-L 0:db:5432", `invalid local port in -L spec: "0:db:5432"`},
		{"-L local port 70000", "-L 70000:db:5432", `invalid local port in -L spec: "70000:db:5432"`},
		{"-L explicit empty bind", "-L :8080:db:5432", `empty bind address in -L spec: ":8080:db:5432"`},
		{"-L bad bind host", "-L my_host:8080:db:5432", `invalid bind address in -L spec: "my_host:8080:db:5432"`},
		{"-L empty destination host", "-L 8080::5432", `empty destination host in -L spec: "8080::5432"`},
		{"-L bad destination host", "-L 8080:my_host:5432", `invalid destination host in -L spec: "8080:my_host:5432"`},
		{"-L dotted all-numeric dst host", "-L 8080:1.2.3:5432", `invalid destination host in -L spec: "8080:1.2.3:5432"`},
		{"-L non-numeric destination port", "-L 8080:db:xx", `invalid destination port in -L spec: "8080:db:xx"`},
		{"-L destination port 0", "-L 8080:db:0", `invalid destination port in -L spec: "8080:db:0"`},
		{"-L destination port 70000", "-L 8080:db:70000", `invalid destination port in -L spec: "8080:db:70000"`},
		{"-L negative destination port", "-L 8080:db:-1", `invalid destination port in -L spec: "8080:db:-1"`},
		{"-L missing value", "-o ServerAliveInterval=5 -L", "missing value for -L"},
		{"-L bracketed IPv6 destination", "-L 8080:[::1]:5432", "IPv6 addresses are not supported in Extra Args (v1 limitation)"},
		{"-L bare IPv6 destination (fails part count)", "-L 8080:fe80::1:5432", `invalid -L spec: "8080:fe80::1:5432" (expected [bind:]localPort:dstHost:dstPort)`},

		// Malformed -D.
		{"-D non-numeric port", "-D 10k8", `invalid local port in -D spec: "10k8"`},
		{"-D port 0", "-D 0", `invalid local port in -D spec: "0"`},
		{"-D port 70000", "-D 70000", `invalid local port in -D spec: "70000"`},
		{"-D too many parts", "-D 1:2:3", `invalid -D spec: "1:2:3" (expected [bind:]localPort)`},
		{"-D explicit empty bind", "-D :1080", `empty bind address in -D spec: ":1080"`},
		{"-D bad bind host", "-D my_host:1080", `invalid bind address in -D spec: "my_host:1080"`},
		{"-D missing value", "-o ServerAliveInterval=5 -D", "missing value for -D"},
		{"-D IPv6 bind", "-D [::1]:1080", "IPv6 addresses are not supported in Extra Args (v1 limitation)"},

		// Malformed -o.
		{"-o unknown key", "-o Compression=yes", `unsupported -o key "Compression"`},
		{"-o key case sensitive", "-o serveraliveinterval=5", `unsupported -o key "serveraliveinterval"`},
		{"-o without equals", "-o Compression", `invalid -o value: "Compression" (expected Key=Value)`},
		{"-o empty key", "-o =5", `invalid -o value: "=5" (expected Key=Value)`},
		{"-o negative interval", "-o ServerAliveInterval=-1", `invalid value for -o ServerAliveInterval: "-1" (expected a non-negative integer)`},
		{"-o non-numeric interval", "-o ServerAliveInterval=x", `invalid value for -o ServerAliveInterval: "x" (expected a non-negative integer)`},
		{"-o empty interval", "-o ServerAliveInterval=", `invalid value for -o ServerAliveInterval: "" (expected a non-negative integer)`},
		{"-o non-numeric timeout", "-o ConnectTimeout=fast", `invalid value for -o ConnectTimeout: "fast" (expected a non-negative integer)`},
		{"-o bad host key checking", "-o StrictHostKeyChecking=yes", `invalid value for -o StrictHostKeyChecking: "yes" (expected "no" or "ask")`},
		{"-o empty host key checking", "-o StrictHostKeyChecking=", `invalid value for -o StrictHostKeyChecking: "" (expected "no" or "ask")`},
		{"-o missing value", "-o", "missing value for -o"},

		// Unbalanced quotes.
		{"unbalanced single quote", `-L '8080:db:5432`, "unbalanced quote"},
		{"unbalanced double quote", `-L "8080:db:5432`, "unbalanced quote"},
		{"unbalanced quote alone", `"`, "unbalanced quote"},

		// Malformed ProxyJump.
		{"ProxyJump empty host", "ProxyJump=", `invalid ProxyJump spec: "" (expected [user@]host[:port])`},
		{"ProxyJump empty host after @", "ProxyJump=bob@", `invalid ProxyJump spec: "bob@" (expected [user@]host[:port])`},
		{"ProxyJump empty user", "ProxyJump=@jump", `invalid ProxyJump spec: "@jump" (expected [user@]host[:port])`},
		{"ProxyJump double @", "ProxyJump=a@b@jump", `invalid ProxyJump spec: "a@b@jump" (expected [user@]host[:port])`},
		{"ProxyJump trailing colon", "ProxyJump=jump:", `invalid ProxyJump spec: "jump:" (expected [user@]host[:port])`},
		{"ProxyJump port 0", "ProxyJump=jump:0", `invalid ProxyJump spec: "jump:0" (expected [user@]host[:port])`},
		{"ProxyJump port 70000", "ProxyJump=jump:70000", `invalid ProxyJump spec: "jump:70000" (expected [user@]host[:port])`},
		{"ProxyJump non-numeric port", "ProxyJump=jump:abc", `invalid ProxyJump spec: "jump:abc" (expected [user@]host[:port])`},
		{"ProxyJump bad host", "ProxyJump=my_host", `invalid ProxyJump spec: "my_host" (expected [user@]host[:port])`},
		{"ProxyJump bracketed IPv6", "ProxyJump=[::1]:2222", "IPv6 addresses are not supported in Extra Args (v1 limitation)"},
		{"duplicate ProxyJump", "ProxyJump=a ProxyJump=b", "duplicate ProxyJump (only one is allowed)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(tt.spec)
			if err == nil {
				t.Fatalf("Parse(%q) = nil error, want %q", tt.spec, tt.wantErr)
			}
			if err.Error() != tt.wantErr {
				t.Fatalf("Parse(%q) error = %q, want %q", tt.spec, err.Error(), tt.wantErr)
			}
		})
	}
}

func TestTokenizer(t *testing.T) {
	tests := []struct {
		name    string
		spec    string
		want    []string
		wantErr bool
	}{
		{"empty spec", "", []string{}, false},
		{"whitespace only", "  \t\n", []string{}, false},
		{"split on spaces", "-L 8080:db:5432", []string{"-L", "8080:db:5432"}, false},
		{"multiple whitespace kinds", "-L\t8080:db:5432\n -D  1080", []string{"-L", "8080:db:5432", "-D", "1080"}, false},
		{"double quotes stripped, space still splits", `"-L 127.0.0.1:8080:db:5432"`, []string{"-L", "127.0.0.1:8080:db:5432"}, false},
		{"single quotes stripped, space still splits", `'-L 127.0.0.1:8080:db:5432'`, []string{"-L", "127.0.0.1:8080:db:5432"}, false},
		{"quoted whitespace runs collapse to a split", `"a  b"`, []string{"a", "b"}, false},
		{"quote does not close on the other kind", `"'a' 'b'"`, []string{"'a'", "'b'"}, false},
		{"concatenated quoted and plain parts", `"ab"cd`, []string{"abcd"}, false},
		{"empty quotes vanish", `"" -D 1080`, []string{"-D", "1080"}, false},
		{"backslash is literal, not an escape", `a\ b`, []string{`a\`, "b"}, false},
		{"backslash inside quotes is literal", `"a\"`, []string{`a\`}, false},
		{"unbalanced double quote", `"abc`, nil, true},
		{"unbalanced single quote", `'abc`, nil, true},
		{"unbalanced quote after a closed one", `"a" b'c`, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tokenize(tt.spec)
			if tt.wantErr {
				if !errors.Is(err, ErrUnbalancedQuote) {
					t.Fatalf("tokenize(%q) err = %v, want ErrUnbalancedQuote", tt.spec, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("tokenize(%q) unexpected error: %v", tt.spec, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("tokenize(%q) = %#v, want %#v", tt.spec, got, tt.want)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	valid := []string{
		"",
		"   ",
		"-L 8080:db:5432",
		"-L 127.0.0.1:8080:db:5432 -D 1080",
		"-o ServerAliveInterval=30 -o ServerAliveCountMax=5 -o ConnectTimeout=10 -o StrictHostKeyChecking=no",
		"ProxyJump=bob@jump:2222",
	}
	for _, spec := range valid {
		if err := Validate(spec); err != nil {
			t.Fatalf("Validate(%q) = %v, want nil", spec, err)
		}
	}
	// Invalid specs surface the parser error verbatim.
	for _, bad := range []struct{ spec, want string }{
		{"-A", "unsupported flag -A"},
		{"-L 8080:db:0", `invalid destination port in -L spec: "8080:db:0"`},
		{"-o Compression=yes", `unsupported -o key "Compression"`},
		{"-L '8080:db:5432", "unbalanced quote"},
	} {
		err := Validate(bad.spec)
		if err == nil || err.Error() != bad.want {
			t.Fatalf("Validate(%q) = %v, want %q", bad.spec, err, bad.want)
		}
	}
}

func TestValidatePortBoundary(t *testing.T) {
	if err := Validate("-D 65535"); err != nil {
		t.Fatalf("upper port bound must pass: %v", err)
	}
	if err := Validate("-D 65536"); err == nil {
		t.Fatal("port above 65535 must fail")
	}
}
