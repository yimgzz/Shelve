// Package app is the application composition root (master plan §5): it
// wires config, store, vault and the SSH engine and owns the
// master-password lifecycle (unlock, lock, zeroization).
//
// Phase 1 wires only config and the Wails service layer; store, vault and
// engine hooks land in Phases 2-3. This package must NOT import the Wails
// API — all Wails usage stays in main.go and internal/wailsvc (§11).
package app

import (
	"dummy-ssh-manager/internal/config"
	"dummy-ssh-manager/internal/wailsvc"
)

// Version is the application version. Keep in sync with build/config.yml
// and build/linux/nfpm/nfpm.yaml.
const Version = "0.1.0"

// App is the composition root shared by all Wails services.
type App struct {
	appService *wailsvc.AppService
}

// New constructs the app and loads settings so configuration errors
// surface at startup (master plan §4).
func New() (*App, error) {
	if _, err := config.Load(); err != nil {
		return nil, err
	}
	return &App{
		appService: wailsvc.NewAppService(Version),
	}, nil
}

// AppService returns the Wails-facing app service.
func (a *App) AppService() *wailsvc.AppService {
	return a.appService
}

// Shutdown flushes deferred state on application exit. Vault store
// flush/close hooks land in Phase 2 (master plan §4: flush on exit).
func (a *App) Shutdown() {}
