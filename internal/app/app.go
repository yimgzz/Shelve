// Package app is the application composition root (master plan §5): it
// wires config, vault and store and owns the master-password lifecycle
// (unlock loads the tree, lock flushes + zeroizes). It must NOT import
// the Wails API — all Wails usage stays in main.go and internal/wailsvc
// (§11); Go→JS events go through the Emitter wired by main.go.
package app

import (
	"context"
	"log"
	"time"

	"shelve/internal/config"
	"shelve/internal/sftp"
	"shelve/internal/sshengine"
	"shelve/internal/sshx/knownhosts"
	"shelve/internal/store"
	"shelve/internal/vault"
	"shelve/internal/wailsvc"
)

// Version is the application version. Keep in sync with build/config.yml
// and build/linux/nfpm/nfpm.yaml.
const Version = "0.1.0"

// exitShutdownTimeout bounds the engine teardown inside Shutdown on
// app exit.
const exitShutdownTimeout = 3 * time.Second

// App is the composition root shared by all Wails services.
type App struct {
	vault   *vault.Vault
	store   *store.Store
	engine  *sshengine.Manager
	sftpMgr *sftp.Manager

	appService        *wailsvc.AppService
	vaultService      *wailsvc.VaultService
	sessionService    *wailsvc.SessionService
	credentialService *wailsvc.CredentialService
	terminalService   *wailsvc.TerminalService
	sftpService       *wailsvc.SftpService
	emitter           *wailsvc.LateEmitter
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

	kh, err := knownhosts.New(config.File(config.KnownHostsFileName))
	if err != nil {
		return nil, err
	}
	engine := sshengine.New(emit, kh)

	// SFTP per-tab clients ride on the engine's active connections (master
	// plan §5). The manager is attached to the engine structurally and
	// registers its per-tab closer so clients die with their tab.
	sftpMgr := sftp.New(config.File(config.TmpDirName), emit)
	sftpMgr.Attach(engine)
	engine.OnTabClosed(sftpMgr.HandleTabClosed)

	// Stale sweep on start: a previous crash could have left editor temps in
	// tmp/ (master plan §8.8). Single-instance app (A7) → safe to delete all.
	if err := sftpMgr.Cleanup(); err != nil {
		log.Printf("app: sftp tmp sweep on start: %v", err)
	}

	return &App{
		vault:             v,
		store:             st,
		engine:            engine,
		sftpMgr:           sftpMgr,
		emitter:           emit,
		appService:        wailsvc.NewAppService(Version),
		vaultService:      wailsvc.NewVaultService(v, st, engine, sftpMgr, emit),
		sessionService:    wailsvc.NewSessionService(st, v, engine, emit),
		credentialService: wailsvc.NewCredentialService(st, v),
		terminalService:   wailsvc.NewTerminalService(st, v, engine),
		sftpService:       wailsvc.NewSftpService(v, sftpMgr),
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

// CredentialService returns the Wails-facing credential service.
func (a *App) CredentialService() *wailsvc.CredentialService {
	return a.credentialService
}

// TerminalService returns the Wails-facing terminal-tab service.
func (a *App) TerminalService() *wailsvc.TerminalService {
	return a.terminalService
}

// SftpService returns the Wails-facing SFTP service.
func (a *App) SftpService() *wailsvc.SftpService {
	return a.sftpService
}

// SetEmitter wires the Wails-backed event emitter. Called from main.go
// after the runtime is constructed (before Run).
func (a *App) SetEmitter(e wailsvc.Emitter) {
	a.emitter.Set(e)
}

// Shutdown disconnects every live session, then flushes deferred state
// on application exit (master plan §5 order: engine before the store
// flush; §4: flush on exit).
func (a *App) Shutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), exitShutdownTimeout)
	a.engine.Shutdown(ctx)
	cancel()
	// Release any SFTP clients the per-tab teardown hook missed (idempotent),
	// then kill live editor process groups and sweep tmp/ (master plan §8.8).
	a.sftpMgr.CloseAll()
	if err := a.sftpMgr.Cleanup(); err != nil {
		log.Printf("app: sftp cleanup on shutdown: %v", err)
	}
	if a.vault.IsUnlocked() {
		if err := a.store.Flush(); err != nil {
			log.Printf("app: flush on exit failed: %v", err)
		}
	}
}
