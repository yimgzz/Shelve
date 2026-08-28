// Package app is the application composition root (master plan §5): it
// wires config, vault and store and owns the master-password lifecycle
// (unlock loads the tree, lock flushes + zeroizes). It must NOT import
// the Wails API — all Wails usage stays in main.go and internal/wailsvc
// (§11); Go→JS events go through the Emitter wired by main.go.
package app

import (
	"log"

	"dummy-ssh-manager/internal/config"
	"dummy-ssh-manager/internal/store"
	"dummy-ssh-manager/internal/vault"
	"dummy-ssh-manager/internal/wailsvc"
)

// Version is the application version. Keep in sync with build/config.yml
// and build/linux/nfpm/nfpm.yaml.
const Version = "0.1.0"

// App is the composition root shared by all Wails services.
type App struct {
	vault *vault.Vault
	store *store.Store

	appService     *wailsvc.AppService
	vaultService   *wailsvc.VaultService
	sessionService *wailsvc.SessionService
	emitter        *wailsvc.LateEmitter
}

// New constructs the app:
//  1. loads settings so configuration errors surface at startup (§4);
//  2. probes the vault file for the startup state machine (§4: no file
//     → create pending, file present → locked pending);
//  3. wires the store's debounced persistence callback to vault.Save.
//
// The Wails runtime does not exist yet at this point, so events flow
// through a LateEmitter that main.go wires after the runtime is created.
func New() (*App, error) {
	if _, err := config.Load(); err != nil {
		return nil, err
	}

	v := vault.New()
	if _, err := v.Probe(config.File(config.VaultFileName)); err != nil {
		return nil, err
	}
	st := store.New(func(payload []byte) error {
		return v.Save(payload)
	})
	emit := &wailsvc.LateEmitter{}

	return &App{
		vault:          v,
		store:          st,
		emitter:        emit,
		appService:     wailsvc.NewAppService(Version),
		vaultService:   wailsvc.NewVaultService(v, st, emit),
		sessionService: wailsvc.NewSessionService(st, v, emit),
	}, nil
}

// AppService returns the Wails-facing app service.
func (a *App) AppService() *wailsvc.AppService {
	return a.appService
}

// VaultService returns the Wails-facing vault service.
func (a *App) VaultService() *wailsvc.VaultService {
	return a.vaultService
}

// SessionService returns the Wails-facing session-tree service.
func (a *App) SessionService() *wailsvc.SessionService {
	return a.sessionService
}

// SetEmitter wires the Wails-backed event emitter. Called from main.go
// after the runtime is constructed (before Run).
func (a *App) SetEmitter(e wailsvc.Emitter) {
	a.emitter.Set(e)
}

// Shutdown flushes deferred state on application exit
// (master plan §4: flush on exit).
func (a *App) Shutdown() {
	if a.vault.IsUnlocked() {
		if err := a.store.Flush(); err != nil {
			log.Printf("app: flush on exit failed: %v", err)
		}
	}
}
