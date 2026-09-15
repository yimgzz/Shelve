// Package app is the application composition root (master plan §5): it
// wires config, vault and store and owns the master-password lifecycle
// (unlock loads the tree, lock flushes + zeroizes). It must NOT import a
// GUI toolkit; the services live in internal/api and are served by
// internal/bridge, which also receives Go→JS events through the Emitter
// wired by the entry point.
package app

import (
	"context"
	"encoding/base64"
	"log"
	"time"

	"shelve/internal/api"
	"shelve/internal/config"
	"shelve/internal/monitor"
	"shelve/internal/sftp"
	"shelve/internal/sshengine"
	"shelve/internal/sshx/knownhosts"
	"shelve/internal/store"
	"shelve/internal/termws"
	"shelve/internal/vault"
)

// Version is the application version. Keep in sync with the root
// package.json `version` (master plan §7).
const Version = "0.1.0"

// exitShutdownTimeout bounds the engine teardown inside Shutdown on
// app exit.
const exitShutdownTimeout = 3 * time.Second

// App is the composition root shared by all API services.
type App struct {
	vault     *vault.Vault
	store     *store.Store
	engine    *sshengine.Manager
	sftpMgr   *sftp.Manager
	monMgr    *monitor.Manager
	termwsSrv *termws.Server

	appService        *api.AppService
	vaultService      *api.VaultService
	sessionService    *api.SessionService
	credentialService *api.CredentialService
	jumpHostService   *api.JumpHostService
	terminalService   *api.TerminalService
	sftpService       *api.SftpService
	monitorService    *api.MonitorService
	emitter           *api.LateEmitter
}

// New constructs the app:
//  1. loads settings so configuration errors surface at startup (§4);
//  2. probes the vault file for the startup state machine (§4: no file
//     → create pending, file present → locked pending);
//  3. wires the store's debounced persistence callback to vault.Save.
//
// The bridge does not exist yet at this point, so events flow through a
// LateEmitter that the entry point wires once the bridge is constructed.
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
	emit := &api.LateEmitter{}

	kh, err := knownhosts.New(config.File(config.KnownHostsFileName))
	if err != nil {
		return nil, err
	}
	engine := sshengine.New(emit, kh)

	// Terminal bytes ride the plan P005 binary WebSocket, not the event
	// transport: the bridge mounts this server listener-less at /terminal on
	// its own loopback listener. Here the server is wired as the engine's
	// data sink and input path; the listener itself is owned by the bridge.
	termwsSrv := termws.NewServer()
	termwsSrv.SetInputHandler(engine.WriteRaw)
	// Safety net (plan P005): if the client can never reach the loopback
	// socket, output falls back to the legacy terminal:data events instead
	// of stalling every tab on a socket that will never arrive. The bridge
	// routes those fallback events over /rpc.
	termwsSrv.SetFallback(func(tabID string, data []byte) {
		emit.Emit(sshengine.EventTerminalData, sshengine.TerminalDataPayload{
			TabID: tabID,
			Data:  base64.StdEncoding.EncodeToString(data),
		})
	})
	engine.SetDataSink(termwsSrv)

	// SFTP per-tab clients ride on the engine's active connections (master
	// plan §5). The manager is attached to the engine structurally and
	// registers its per-tab closer so clients die with their tab.
	sftpMgr := sftp.New(config.File(config.TmpDirName), emit)
	sftpMgr.Attach(engine)

	// Plan P004: per-tab system monitor (active-tab only; lifecycle driven
	// by the frontend via MonitorService.Start/Stop). Also rides the engine
	// connections and registers its per-tab closer so ticker goroutines die
	// with their tab. The engine holds ONE OnTabClosed hook, so the two
	// closers are composed here (plan P004 D3).
	monMgr := monitor.New(emit)
	monMgr.Attach(engine)
	engine.OnTabClosed(func(tabID string) {
		sftpMgr.HandleTabClosed(tabID)
		monMgr.HandleTabClosed(tabID)
	})

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
		monMgr:            monMgr,
		termwsSrv:         termwsSrv,
		emitter:           emit,
		appService:        api.NewAppService(Version),
		vaultService:      api.NewVaultService(v, st, engine, sftpMgr, emit),
		sessionService:    api.NewSessionService(st, v, engine, emit),
		credentialService: api.NewCredentialService(st, v),
		jumpHostService:   api.NewJumpHostService(st, v),
		terminalService:   api.NewTerminalService(st, v, engine),
		sftpService:       api.NewSftpService(v, sftpMgr),
		monitorService:    api.NewMonitorService(v, monMgr),
	}, nil
}

// AppService returns the app service.
func (a *App) AppService() *api.AppService {
	return a.appService
}

// VaultService returns the vault service.
func (a *App) VaultService() *api.VaultService {
	return a.vaultService
}

// SessionService returns the session-tree service.
func (a *App) SessionService() *api.SessionService {
	return a.sessionService
}

// CredentialService returns the credential service.
func (a *App) CredentialService() *api.CredentialService {
	return a.credentialService
}

// JumpHostService returns the saved-jump-host service.
func (a *App) JumpHostService() *api.JumpHostService {
	return a.jumpHostService
}

// TerminalService returns the terminal-tab service.
func (a *App) TerminalService() *api.TerminalService {
	return a.terminalService
}

// SftpService returns the SFTP service.
func (a *App) SftpService() *api.SftpService {
	return a.sftpService
}

// MonitorService returns the system-monitor service.
func (a *App) MonitorService() *api.MonitorService {
	return a.monitorService
}

// TerminalWS returns the plan P005 terminal I/O WebSocket server. The
// bridge mounts it listener-less at /terminal on its loopback listener.
func (a *App) TerminalWS() *termws.Server {
	return a.termwsSrv
}

// SetEmitter wires the transport-backed event emitter. Called from the
// entry point once the bridge (or another Emitter) is constructed.
func (a *App) SetEmitter(e api.Emitter) {
	a.emitter.Set(e)
}

// Shutdown disconnects every live session, then flushes deferred state
// on application exit (master plan §5 order: engine before the store
// flush; §4: flush on exit).
func (a *App) Shutdown() {
	// Drop the terminal transport first so blocked output writers release
	// before the engine tears down its connections.
	_ = a.termwsSrv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), exitShutdownTimeout)
	a.engine.Shutdown(ctx)
	cancel()
	// Release any SFTP clients the per-tab teardown hook missed (idempotent),
	// then kill live editor process groups and sweep tmp/ (master plan §8.8).
	a.sftpMgr.CloseAll()
	if err := a.sftpMgr.Cleanup(); err != nil {
		log.Printf("app: sftp cleanup on shutdown: %v", err)
	}
	// Stop any monitor ticker the per-tab teardown hook missed (idempotent).
	a.monMgr.CloseAll()
	if a.vault.IsUnlocked() {
		if err := a.store.Flush(); err != nil {
			log.Printf("app: flush on exit failed: %v", err)
		}
	}
}
