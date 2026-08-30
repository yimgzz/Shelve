// Command seed is a permanent QA/dev tool (Phase 4b, task 6; master plan
// §9 perf smoke). It writes a deterministic 300-session TEST vault into
// the app config dir (or a --dir override) so the tree/search/tabs editor
// can be exercised at scale, and prints the master password the app needs
// to unlock it. `make unseed` removes the seeded vault.json + known_hosts
// while keeping settings.json. It must never be used in production.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"dummy-ssh-manager/internal/config"
	"dummy-ssh-manager/internal/model"
	"dummy-ssh-manager/internal/vault"
)

// seedPassword is the documented master password for the QA vault: after
// `make seed`, unlock the app with exactly this value.
const seedPassword = "seed-master-password"

// seedSessions is the fixture size used by manual QA and the master-plan
// perf smoke budget (300 nodes).
const seedSessions = 300

func main() {
	dir := flag.String("dir", "", "config dir to seed/unseed (default: XDG config dir)")
	force := flag.Bool("force", false, "overwrite an existing vault.json")
	unseed := flag.Bool("unseed", false, "remove the seeded vault.json + known_hosts (keeps settings)")
	flag.Parse()

	if *unseed {
		if err := unseedRun(*dir); err != nil {
			fail(err)
		}
		return
	}
	if err := seedRun(*dir, *force); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "seed:", err)
	os.Exit(1)
}

func targetDir(dir string) string {
	if dir != "" {
		return dir
	}
	return config.Path()
}

func seedRun(dir string, force bool) error {
	target := targetDir(dir)
	vaultPath := filepath.Join(target, config.VaultFileName)

	if !force {
		if _, err := os.Stat(vaultPath); err == nil {
			return fmt.Errorf("%s already exists (run 'make unseed' first, or pass --force)", vaultPath)
		}
	}

	payload, err := json.Marshal(model.GenerateFixture(seedSessions))
	if err != nil {
		return err
	}

	v := vault.New()
	if err := v.Create(vaultPath, seedPassword, payload); err != nil {
		return err
	}

	fmt.Printf("seeded %d-session test vault at %s\n", seedSessions, vaultPath)
	fmt.Printf("unlock the app with master password: %s\n", seedPassword)
	return nil
}

func unseedRun(dir string) error {
	target := targetDir(dir)
	var removed []string
	for _, name := range []string{config.VaultFileName, config.KnownHostsFileName} {
		p := filepath.Join(target, name)
		if err := os.Remove(p); err == nil {
			removed = append(removed, p)
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	if len(removed) == 0 {
		fmt.Println("nothing to remove (no seeded vault.json / known_hosts present)")
		return nil
	}
	for _, p := range removed {
		fmt.Println("removed", p)
	}
	fmt.Println("settings.json left untouched")
	return nil
}
