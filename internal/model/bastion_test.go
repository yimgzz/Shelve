package model

// Plan P009: bastion-style jump host validation rules (§3).

import (
	"strings"
	"testing"
)

// bastionSession returns a valid session whose only jump hop is a bastion:
// port 22, hostname target, bastion hop last.
func bastionSession() Session {
	s := validSession()
	s.JumpHosts = []JumpHost{
		{Host: "bastion.corp.example.com", Port: 22, User: "relay",
			Bastion: true, Auth: Auth{Type: AuthPassword, Password: "ldap-pw"}},
	}
	return s
}

func wantBastionErr(t *testing.T, sess Session, want string) {
	t.Helper()
	wantFieldErr(t, sess, want)
}

func TestBastionSessionValidationRules(t *testing.T) {
	t.Run("valid bastion session passes", func(t *testing.T) {
		s := bastionSession()
		if err := s.Validate(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("empty target auth allowed in bastion mode", func(t *testing.T) {
		s := bastionSession()
		s.Auth = Auth{} // Type is AuthPassword (zero); nothing set
		if err := s.Validate(); err != nil {
			t.Fatalf("empty target auth must be valid with a bastion: %v", err)
		}
	})

	t.Run("set target auth still must be a consistent XOR", func(t *testing.T) {
		s := bastionSession()
		s.Auth = Auth{Type: AuthPassword, Password: "pw", KeyPath: "/k"}
		wantBastionErr(t, s, "session.auth: exactly one of password or keyPath may be set")
	})

	t.Run("set target auth with mismatched type still rejected", func(t *testing.T) {
		s := bastionSession()
		s.Auth = Auth{Type: AuthKey, Password: "pw"}
		wantBastionErr(t, s, "session.auth: keyPath is required when authType is key, not password")
	})

	t.Run("non-bastion session still requires exactly one auth", func(t *testing.T) {
		s := validSession()
		s.Auth = Auth{Type: AuthPassword}
		wantFieldErr(t, s, "session.auth: exactly one of password or keyPath must be set")
	})

	t.Run("two bastion hops rejected on both", func(t *testing.T) {
		s := bastionSession()
		s.JumpHosts = append(s.JumpHosts, JumpHost{
			Host: "bastion2.corp.example.com", Port: 22, User: "relay2",
			Bastion: true, Auth: Auth{Type: AuthPassword, Password: "p2"},
		})
		err := s.Validate()
		if err == nil {
			t.Fatal("expected errors")
		}
		msg := err.Error()
		if !strings.Contains(msg, "session.jumpHosts[0].bastion: at most one bastion jump host is allowed") ||
			!strings.Contains(msg, "session.jumpHosts[1].bastion: at most one bastion jump host is allowed") {
			t.Fatalf("both bastion hops must be flagged: %s", msg)
		}
	})

	t.Run("bastion must be the last hop", func(t *testing.T) {
		s := bastionSession()
		s.JumpHosts = []JumpHost{
			{Host: "bastion.corp.example.com", Port: 22, User: "relay",
				Bastion: true, Auth: Auth{Type: AuthPassword, Password: "ldap-pw"}},
			{Host: "other.example.com", Port: 22, User: "x",
				Auth: Auth{Type: AuthPassword, Password: "pw"}},
		}
		wantBastionErr(t, s, "session.jumpHosts[0].bastion: the bastion jump host must be the last hop")
	})

	t.Run("target port must be 22", func(t *testing.T) {
		s := bastionSession()
		s.Port = 2222
		wantBastionErr(t, s, "session.port: bastion target requires port 22 (v1 limitation)")
	})

	t.Run("IPv6 target rejected in bastion mode", func(t *testing.T) {
		s := bastionSession()
		s.Host = "fe80::1"
		wantBastionErr(t, s, "session.host: bastion target requires a hostname or IPv4 (no IPv6 in bastion mode)")
	})

	t.Run("ProxyJump rejected with a bastion hop", func(t *testing.T) {
		s := bastionSession()
		s.ExtraArgs = "ProxyJump=bob@jump.example.com:2222"
		wantBastionErr(t, s, "session.extraArgs: a bastion jump host cannot be combined with ProxyJump")
	})

	t.Run("bastion hop user must not contain @", func(t *testing.T) {
		s := bastionSession()
		s.JumpHosts[0].User = "a@b"
		wantBastionErr(t, s, "session.jumpHosts[0].user: bastion user must not contain '@' (the target is embedded in the username)")
	})

	t.Run("non-bastion chains keep direct semantics", func(t *testing.T) {
		// Old payload shape: no bastion flag anywhere. Port != 22, IPv6
		// target, ProxyJump and empty auth are all rejected/accepted
		// exactly as before the bastion rules existed.
		s := validSession()
		s.Port = 2222
		s.Host = "fe80::1"
		s.ExtraArgs = "ProxyJump=bob@jump.example.com:2222"
		s.JumpHosts = []JumpHost{
			{Host: "jmp1.example.com", Port: 22, User: "tunnel",
				Auth: Auth{Type: AuthPassword, Password: "pw"}},
			{Host: "jmp2.example.com", Port: 22, User: "tunnel2",
				Auth: Auth{Type: AuthKey, KeyPath: "/k"}},
		}
		if err := s.Validate(); err != nil {
			t.Fatalf("non-bastion session must not trigger bastion rules: %v", err)
		}
	})
}

func TestBastionJumpHostValidationError(t *testing.T) {
	j := JumpHost{Host: "x.example.com", Port: 22, User: "a@b",
		Bastion: true, Auth: Auth{Type: AuthPassword, Password: "pw"}}
	err := j.Validate()
	if err == nil {
		t.Fatal("expected @ error in bastion user")
	}
	if !strings.Contains(err.Error(), "jumpHost.user: bastion user must not contain '@'") {
		t.Fatalf("prefix wrong: %v", err)
	}
}

func TestBastionSavedJumpHostValidationError(t *testing.T) {
	j := SavedJumpHost{
		ID: "JH000000000000000000000000A", Name: "b",
		Host: "x.example.com", Port: 22, User: "a@b",
		Bastion: true, Auth: Auth{Type: AuthPassword, Password: "pw"},
	}
	err := j.Validate()
	if err == nil {
		t.Fatal("expected @ error in bastion saved jump host user")
	}
	if !strings.Contains(err.Error(), "bastion user must not contain '@'") {
		t.Fatalf("error missing message: %v", err)
	}

	ok := j
	ok.User = "relay"
	if err := ok.Validate(); err != nil {
		t.Fatalf("clean bastion saved jump host rejected: %v", err)
	}
}
