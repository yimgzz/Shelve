package bridge

import (
	"reflect"
	"sort"
	"testing"

	"shelve/internal/app"
)

// TestServiceSurfaceGolden is the anti-drift gate (master plan §5, §11): the
// bridge registry must expose exactly the documented service surface. Adding
// or removing a service method requires updating this test on purpose. It
// registers through the same RegisterAll path as the production entry point.
func TestServiceSurfaceGolden(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	a, err := app.New()
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	t.Cleanup(a.Shutdown)

	s := New(a.TerminalWS())
	t.Cleanup(func() { _ = s.Close() })

	s.RegisterAll(a)

	want := map[string][]string{
		"AppService":        {"GetVersion", "GetSettings", "SaveSettings"},
		"VaultService":      {"Status", "CreateVault", "Unlock", "Lock", "ApproveHostKey", "RejectHostKey", "SubmitKeyPassphrase", "SubmitKbdintResponse", "CancelKbdint"},
		"SessionService":    {"Tree", "Search", "CreateFolder", "RenameFolder", "MoveNode", "DeleteNode", "CreateSession", "Session", "UpdateSession", "DuplicateSession", "ValidateExtraArgs", "TestConnection"},
		"CredentialService": {"List", "Create", "Update", "Delete", "Get", "Usage"},
		"JumpHostService":   {"List", "Create", "Update", "Delete", "Get", "Usage"},
		"TerminalService":   {"Connect", "Disconnect", "Write", "Resize", "Reconnect"},
		"SftpService":       {"IsActive", "List", "Mkdir", "Rename", "Remove", "Upload", "Download", "DownloadTo", "EditRemoteText", "CancelEdit"},
		"MonitorService":    {"Start", "Stop"},
		"TransferService":   {"Export", "Import"},
	}

	if len(s.services) != len(want) {
		t.Fatalf("registered %d services, want %d", len(s.services), len(want))
	}

	for svc, methods := range want {
		entry, ok := s.services[svc]
		if !ok {
			t.Fatalf("service %s not registered", svc)
		}
		got := make([]string, 0, len(entry.methods))
		for name := range entry.methods {
			got = append(got, name)
		}
		sort.Strings(got)
		wantSorted := append([]string(nil), methods...)
		sort.Strings(wantSorted)
		if !reflect.DeepEqual(got, wantSorted) {
			t.Fatalf("%s surface = %v, want %v", svc, got, wantSorted)
		}
	}
}
