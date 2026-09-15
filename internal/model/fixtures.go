package model

import "fmt"

// GenerateFixture builds a deterministic tree payload with the given number
// of sessions spread over 10 top-level folders with 2 subfolders each
// (30 folders total), plus a few root-level sessions. It is test-only
// (master plan §9 perf smoke) and exported so store/api tests can
// reuse it.
func GenerateFixture(sessionCount int) Payload {
	folders := make([]Folder, 0, 30)
	order := map[string][]string{}
	var root []string
	var subs []string // the 20 subfolders, in creation order

	for i := 0; i < 10; i++ {
		top := Folder{
			ID:   NewID(),
			Name: fmt.Sprintf("Folder-%02d", i+1),
		}
		folders = append(folders, top)
		root = append(root, top.ID)

		for k := 0; k < 2; k++ {
			sub := Folder{
				ID:       NewID(),
				ParentID: top.ID,
				Name:     fmt.Sprintf("Folder-%02d/%c", i+1, 'a'+rune(k)),
			}
			folders = append(folders, sub)
			subs = append(subs, sub.ID)
			order[top.ID] = append(order[top.ID], sub.ID)
		}
	}

	// A handful of named credentials (plan P003 §9): two password bundles
	// and one key bundle. Sessions in the loop below reference them and
	// keep matching inline snapshots so the seeded vault exercises the
	// credential-manager UI out of the box.
	var credentials []Credential
	newCred := func(name, user string, auth Auth) string {
		c := Credential{ID: NewID(), Name: name, User: user, Auth: auth}
		credentials = append(credentials, c)
		return c.ID
	}
	credPasswordID := newCred("prod-admin", "admin",
		Auth{Type: AuthPassword, Password: "fixture-cred-pw-prod-admin"})
	credKeyID := newCred("dev-key", "dev",
		Auth{Type: AuthKey, KeyPath: "/home/user/.ssh/fixture_dev_ed25519"})
	newCred("backup-svc", "backup",
		Auth{Type: AuthPassword, Password: "fixture-cred-pw-backup"})

	// A few saved jump hosts (plan P006 §9): one password and one key
	// variant. Sessions in the loop below reference them and keep matching
	// inline snapshots so the seeded vault exercises the saved-jump-host
	// dropdown and manager UI out of the box.
	var savedJumpHosts []SavedJumpHost
	newSavedJump := func(name, host string, port int, user string, auth Auth) string {
		jh := SavedJumpHost{ID: NewID(), Name: name, Host: host, Port: port, User: user, Auth: auth}
		savedJumpHosts = append(savedJumpHosts, jh)
		return jh.ID
	}
	savedJumpPasswordID := newSavedJump("bastion-prod", "jump-saved-01.example.com", 22, "tunnel",
		Auth{Type: AuthPassword, Password: "fixture-saved-jump-pw-01"})
	savedJumpKeyID := newSavedJump("bastion-backup", "192.168.9.5", 2222, "tunnel",
		Auth{Type: AuthKey, KeyPath: "/home/user/.ssh/jump-saved"})

	sessions := make([]Session, 0, sessionCount)
	for j := 0; j < sessionCount; j++ {
		s := Session{
			ID:   NewID(),
			Name: fmt.Sprintf("Session-%04d", j+1),
			User: "user",
			Port: 22,
		}
		// Every 25th session references a saved credential (plan P003):
		// keep the inline snapshot equal to the credential so the session
		// also works after the credential is deleted.
		switch j % 25 {
		case 0:
			s.CredentialID = credPasswordID
			s.User = "admin"
			s.Auth = Auth{Type: AuthPassword, Password: "fixture-cred-pw-prod-admin"}
		case 5:
			s.CredentialID = credKeyID
			s.User = "dev"
			s.Auth = Auth{Type: AuthKey, KeyPath: "/home/user/.ssh/fixture_dev_ed25519"}
		}
		switch j % 2 {
		case 0: // hostname + password auth
			s.Host = fmt.Sprintf("host-%04d.example.com", j+1)
			s.Auth = Auth{
				Type:     AuthPassword,
				Password: fmt.Sprintf("fixture-password-%04d", j+1),
			}
		default: // IPv4 + key auth, odd ones get a high port
			s.Host = fmt.Sprintf("10.%d.%d.%d", (j/65536)%192+1, (j/256)%256, j%256+1)
			if j%4 == 1 {
				s.Port = 2222
			}
			s.Auth = Auth{
				Type:    AuthKey,
				KeyPath: fmt.Sprintf("/home/user/.ssh/id_ed25519_%04d", j+1),
			}
		}

		// Every 20th session gets a jump host chain (1 or 2 hops).
		switch j % 20 {
		case 0:
			s.JumpHosts = []JumpHost{
				{
					Host: "jump-01.example.com", Port: 22, User: "tunnel",
					Auth: Auth{Type: AuthPassword, Password: "fixture-jump-pw-01"},
				},
			}
		case 4:
			s.JumpHosts = []JumpHost{
				{
					Host: "jump-01.example.com", Port: 22, User: "tunnel",
					Auth: Auth{Type: AuthKey, KeyPath: "/home/user/.ssh/jump1"},
				},
				{
					Host: "192.168.7.10", Port: 2200, User: "tunnel",
					Auth: Auth{Type: AuthPassword, Password: "fixture-jump-pw-02"},
				},
			}
		}

		// Every 25th session references a saved jump host (plan P006): the
		// inline chain holds the saved host's hop as its snapshot so the
		// session also works after the saved host is deleted. Runs after
		// the j%20 chain above, deliberately overriding it where both fire.
		switch j % 25 {
		case 10:
			s.JumpHostRef = savedJumpPasswordID
			s.JumpHosts = []JumpHost{
				{
					Host: "jump-saved-01.example.com", Port: 22, User: "tunnel",
					Auth: Auth{Type: AuthPassword, Password: "fixture-saved-jump-pw-01"},
				},
			}
		case 15:
			s.JumpHostRef = savedJumpKeyID
			s.JumpHosts = []JumpHost{
				{
					Host: "192.168.9.5", Port: 2222, User: "tunnel",
					Auth: Auth{Type: AuthKey, KeyPath: "/home/user/.ssh/jump-saved"},
				},
			}
		}

		// Placement: every 30th session sits at the root level, the rest
		// rotate across the 20 subfolders.
		var parent string
		if j%30 == 29 {
			parent = ""
			root = append(root, s.ID)
		} else {
			parent = subs[j%20]
		}
		s.FolderID = parent
		order[parent] = append(order[parent], s.ID)
		sessions = append(sessions, s)
	}

	// Plan P009: one bastion saved jump host plus one bastion session that
	// references it (P006 path) with the saved hop as its inline snapshot.
	// Exercises the bastion checkbox / validation / prefill UI paths; the
	// fictional target is not dial-able in tests.
	bastionSaved := SavedJumpHost{
		ID:      NewID(),
		Name:    "bastion-mode",
		Host:    "bastion-01.corp.example.com",
		Port:    22,
		User:    "relay",
		Bastion: true,
		Auth:    Auth{Type: AuthPassword, Password: "fixture-bastion-pw"},
	}
	savedJumpHosts = append(savedJumpHosts, bastionSaved)
	bastionSession := Session{
		ID:          NewID(),
		Name:        "Bastion Session",
		User:        "user",
		Port:        22, // bastion target requires 22 (v1)
		Host:        "bastion-target-01.corp.example.com",
		Auth:        Auth{}, // target auth optional in bastion mode
		JumpHostRef: bastionSaved.ID,
		JumpHosts: []JumpHost{
			{
				Host: "bastion-01.corp.example.com", Port: 22, User: "relay", Bastion: true,
				Auth: Auth{Type: AuthPassword, Password: "fixture-bastion-pw"},
			},
		},
	}
	root = append(root, bastionSession.ID)
	order[""] = append(order[""], bastionSession.ID)
	sessions = append(sessions, bastionSession)

	// Keep Folder.Children consistent with the built order lists.
	for i := range folders {
		folders[i].Children = append([]string(nil), order[folders[i].ID]...)
	}
	return Payload{
		Root:           root,
		Folders:        folders,
		Sessions:       sessions,
		Credentials:    credentials,
		SavedJumpHosts: savedJumpHosts,
	}
}
