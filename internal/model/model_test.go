package model

import (
	"regexp"
	"strings"
	"testing"
)

func TestValidHostTable(t *testing.T) {
	cases := []struct {
		host string
		ok   bool
	}{
		{"db.example.com", true},
		{"host01", true},
		{"a.b-c.d", true},
		{"my_host", false}, // underscore is not RFC 1123
		{"-foo", false},
		{"foo-", false},
		{"a..b", false},
		{".foo", false},
		{"foo.", false},
		{"1.2.3.4", true},
		{"255.255.255.255", true},
		{"256.1.1.1", false},
		{"1.2.3.4.5", false}, // dotted all-numeric that is not IPv4
		{"1.2.3", false},     // 3 all-numeric labels, not a valid host
		{":80", false},
		{"::1", true},
		{"fe80::1", true},
		{"[::1]", false}, // brackets are a known_hosts form, not a host field
		{"example.com ssh", false},
		{"", false},
	}
	for _, c := range cases {
		if got := validHost(c.host); got != c.ok {
			t.Errorf("validHost(%q) = %v, want %v", c.host, got, c.ok)
		}
	}

	long := "a" + strings.Repeat("b", 62)          // 63-char label, valid
	longDomain := long + strings.Repeat(".b-c", 3) // 81 chars, under 253
	if !validHost(longDomain) {
		t.Errorf("validHost(%q) = false, want true", longDomain)
	}
	tooLong := strings.Repeat("a1.", 57) // 283 chars > 253
	if validHost(tooLong) {
		t.Errorf("validHost(%q) = true, want false (too long)", tooLong)
	}
}

func TestValidPortBounds(t *testing.T) {
	if !validPort(1) || !validPort(22) || !validPort(65535) {
		t.Fatal("bounds 1 and 65535 must be valid")
	}
	if validPort(0) || validPort(65536) || validPort(-1) {
		t.Fatal("out-of-range port accepted")
	}
}

func validSession() Session {
	return Session{
		Name: "prod-db-01",
		Host: "db.example.com",
		Port: 22,
		User: "alice",
		Auth: Auth{Type: AuthPassword, Password: "s3cr3t"},
	}
}

func wantFieldErr(t *testing.T, sess Session, want string) {
	t.Helper()
	err := sess.Validate()
	if err == nil {
		t.Fatalf("expected error containing %q, got nil", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error %q does not contain %q", err.Error(), want)
	}
}

func TestSessionValidationRules(t *testing.T) {
	t.Run("valid session passes", func(t *testing.T) {
		s := validSession()
		if err := s.Validate(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("empty name", func(t *testing.T) {
		s := validSession()
		s.Name = "   "
		wantFieldErr(t, s, "session.name: must not be empty")
	})

	t.Run("bad host", func(t *testing.T) {
		s := validSession()
		s.Host = "not a host!"
		wantFieldErr(t, s, "session.host: must be a valid hostname, IPv4 or IPv6 address")
	})

	t.Run("port bounds", func(t *testing.T) {
		for _, p := range []int{0, -5, 65536} {
			s := validSession()
			s.Port = p
			wantFieldErr(t, s, "session.port: must be in range 1-65535")
		}
		for _, p := range []int{1, 22, 65535} {
			s := validSession()
			s.Port = p
			if err := s.Validate(); err != nil {
				t.Fatalf("port %d rejected: %v", p, err)
			}
		}
	})

	t.Run("empty user", func(t *testing.T) {
		s := validSession()
		s.User = ""
		wantFieldErr(t, s, "session.user: must not be empty")
	})

	t.Run("auth xor: both set", func(t *testing.T) {
		s := validSession()
		s.Auth = Auth{Type: AuthPassword, Password: "pw", KeyPath: "/k"}
		wantFieldErr(t, s, "session.auth: exactly one of password or keyPath may be set")
	})

	t.Run("auth xor: neither set", func(t *testing.T) {
		s := validSession()
		s.Auth = Auth{Type: AuthPassword}
		wantFieldErr(t, s, "session.auth: exactly one of password or keyPath must be set")
	})

	t.Run("auth type mismatch key declares password set", func(t *testing.T) {
		s := validSession()
		s.Auth = Auth{Type: AuthKey, Password: "pw"}
		wantFieldErr(t, s, "session.auth: keyPath is required when authType is key, not password")
	})

	t.Run("auth type mismatch password declares key set", func(t *testing.T) {
		s := validSession()
		s.Auth = Auth{Type: AuthPassword, KeyPath: "/k"}
		wantFieldErr(t, s, "session.auth: password is required when authType is password, not keyPath")
	})

	t.Run("auth type unknown", func(t *testing.T) {
		s := validSession()
		s.Auth = Auth{Type: AuthType(9), Password: "pw"}
		wantFieldErr(t, s, "session.auth: authType must be password or key")
	})

	t.Run("key auth valid", func(t *testing.T) {
		s := validSession()
		s.Auth = Auth{Type: AuthKey, KeyPath: "/home/a/.ssh/id_ed25519"}
		if err := s.Validate(); err != nil {
			t.Fatalf("unexpected: %v", err)
		}
	})

	t.Run("sftpInitialPath valid values", func(t *testing.T) {
		for _, p := range []string{"", "~", "~/data", "/srv/www", "/tmp"} {
			s := validSession()
			s.SftpInitialPath = p
			if err := s.Validate(); err != nil {
				t.Fatalf("path %q rejected: %v", p, err)
			}
		}
	})

	t.Run("sftpInitialPath rejects relative path", func(t *testing.T) {
		s := validSession()
		s.SftpInitialPath = "data/sub"
		wantFieldErr(t, s, "session.sftpInitialPath: must be empty, absolute, or ~-prefixed")
	})

	t.Run("multiple violations joined", func(t *testing.T) {
		s := Session{}
		err := s.Validate()
		if err == nil {
			t.Fatal("expected errors")
		}
		msg := err.Error()
		for _, want := range []string{"session.name", "session.host", "session.port", "session.user", "session.auth"} {
			if !strings.Contains(msg, want) {
				t.Errorf("joined error missing %q: %s", want, msg)
			}
		}
	})
}

func TestJumpHostValidationRules(t *testing.T) {
	t.Run("valid jump host", func(t *testing.T) {
		j := JumpHost{Host: "jmp.example.com", Port: 22, User: "tunnel",
			Auth: Auth{Type: AuthPassword, Password: "pw"}}
		if err := j.Validate(); err != nil {
			t.Fatalf("unexpected: %v", err)
		}
	})

	t.Run("jump bad fields reported with index prefix", func(t *testing.T) {
		s := validSession()
		s.JumpHosts = []JumpHost{
			{Host: "bad host", Port: 0, User: "", Auth: Auth{Type: AuthPassword}},
			{Host: "10.0.0.1", Port: 65536, User: "ok",
				Auth: Auth{Type: AuthKey, KeyPath: "/k"}},
		}
		err := s.Validate()
		if err == nil {
			t.Fatal("expected errors")
		}
		msg := err.Error()
		for _, want := range []string{
			"session.jumpHosts[0].host",
			"session.jumpHosts[0].port",
			"session.jumpHosts[0].user",
			"session.jumpHosts[0].auth",
			"session.jumpHosts[1].port",
		} {
			if !strings.Contains(msg, want) {
				t.Errorf("error missing %q: %s", want, msg)
			}
		}
	})

	t.Run("standalone jump host uses jumpHost prefix", func(t *testing.T) {
		j := JumpHost{Host: "x", Port: 22, User: "u",
			Auth: Auth{Type: AuthPassword, Password: "p", KeyPath: "/k"}}
		err := j.Validate()
		if err == nil {
			t.Fatal("expected XOR error")
		}
		if !strings.Contains(err.Error(), "jumpHost.auth:") {
			t.Fatalf("prefix wrong: %v", err)
		}
	})
}

func TestSessionExtraArgsValidation(t *testing.T) {
	s := validSession()
	s.ExtraArgs = ""
	if err := s.Validate(); err != nil {
		t.Fatalf("empty extra args must pass: %v", err)
	}
	s.ExtraArgs = "-L 8080:localhost:80 -D 1080 -o StrictHostKeyChecking=no ProxyJump=bob@jump:2222"
	if err := s.Validate(); err != nil {
		t.Fatalf("valid extra args must pass: %v", err)
	}
	s.ExtraArgs = "-R 8080:localhost:80"
	err := s.Validate()
	// The strict parser error is embedded in the field error.
	if !strings.Contains(err.Error(), "session.extraArgs:") ||
		!strings.Contains(err.Error(), "unsupported flag -R") {
		t.Fatalf("want field error wrapping the parser message, got: %v", err)
	}
	s.ExtraArgs = "-L 8080:db"
	err = s.Validate()
	if !strings.Contains(err.Error(), `invalid -L spec: "8080:db"`) {
		t.Fatalf("want field error wrapping the parser message, got: %v", err)
	}
}

func TestNewIDFormatAndUniqueness(t *testing.T) {
	re := regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := NewID()
		if !re.MatchString(id) {
			t.Fatalf("bad ULID format: %q", id)
		}
		if seen[id] {
			t.Fatalf("duplicate ID: %q", id)
		}
		seen[id] = true
	}
}

func TestFolderValidate(t *testing.T) {
	f := Folder{Name: "F"}
	if err := f.Validate(); err != nil {
		t.Fatalf("valid folder rejected: %v", err)
	}
	for _, name := range []string{"", "   "} {
		f := Folder{Name: name}
		err := f.Validate()
		if err == nil || !strings.Contains(err.Error(), "folder.name: must not be empty") {
			t.Fatalf("folder %q: want name error, got %v", name, err)
		}
	}
}

// TestCredentialValidationRules covers the plan P003 §4.1 invariants:
// name required, user required for password bundles, and the same
// password-XOR-key rule as sessions.
func TestCredentialValidationRules(t *testing.T) {
	valid := func() Credential {
		return Credential{
			Name: "prod-admin",
			User: "admin",
			Auth: Auth{Type: AuthPassword, Password: "s3cr3t"},
		}
	}

	t.Run("valid password credential", func(t *testing.T) {
		c := valid()
		if err := c.Validate(); err != nil {
			t.Fatalf("unexpected: %v", err)
		}
	})

	t.Run("valid key credential", func(t *testing.T) {
		c := valid()
		c.Auth = Auth{Type: AuthKey, KeyPath: "/home/a/.ssh/id_ed25519"}
		if err := c.Validate(); err != nil {
			t.Fatalf("unexpected: %v", err)
		}
	})

	t.Run("empty name", func(t *testing.T) {
		c := valid()
		c.Name = "  "
		err := c.Validate()
		if err == nil || !strings.Contains(err.Error(), "credential.name: must not be empty") {
			t.Fatalf("want name error, got %v", err)
		}
	})

	t.Run("empty user", func(t *testing.T) {
		c := valid()
		c.User = ""
		err := c.Validate()
		if err == nil || !strings.Contains(err.Error(), "credential.user: must not be empty") {
			t.Fatalf("want user error, got %v", err)
		}
	})

	t.Run("auth xor: neither set", func(t *testing.T) {
		c := valid()
		c.Auth = Auth{Type: AuthPassword}
		err := c.Validate()
		if err == nil || !strings.Contains(err.Error(), "credential.auth: exactly one of password or keyPath must be set") {
			t.Fatalf("want XOR error, got %v", err)
		}
	})

	t.Run("auth xor: both set", func(t *testing.T) {
		c := valid()
		c.Auth = Auth{Type: AuthPassword, Password: "pw", KeyPath: "/k"}
		err := c.Validate()
		if err == nil || !strings.Contains(err.Error(), "credential.auth: exactly one of password or keyPath may be set") {
			t.Fatalf("want XOR error, got %v", err)
		}
	})
}
