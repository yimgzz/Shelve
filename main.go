package main

import (
	"embed"
	"log"
	"net/http"
	"os"

	"github.com/wailsapp/wails/v3/pkg/application"

	"shelve/internal/app"
	"shelve/internal/wailsvc"
)

// Wails embeds the built frontend (frontend/dist) into the binary and serves
// it to the webview.
//
//go:embed all:frontend/dist
var assets embed.FS

//
//go:embed all:build/appicon.png
var appIcon []byte

func main() {
	// WebKitGTK renderer selection.
	//
	// The GPU dmabuf renderer was briefly the default (it prevents a
	// terminal presentation freeze when moving between mixed-scale monitors
	// on X11). But on GPU/WebKitGTK stacks it exposes the compositor's
	// native clear colour whenever it re-tiles while the window is
	// unfocused: with a terminal canvas open, the whole window flashes
	// light/white every few seconds even fully idle — the dark theme
	// background cannot cover a dropped composited layer (verified
	// 2026-09-12 against the accelerated-compositing clear-colour latch
	// documented in Wails v3's linux webview).
	//
	// Fix: default to software-only rendering (WEBKIT_DISABLE_DMABUF_RENDERER=1)
	// on every session type — the original v1 default and the flash-free
	// path. The residual mixed-DPI canvas presentation freeze on X11 is
	// mitigated by the renderer-recovery passes in frontend/src/terminal/xterm.ts
	// (unpause + char re-measure + full refresh on stuck renderers after
	// resize/move). Users can still force the GPU renderer with
	// WEBKIT_DISABLE_DMABUF_RENDERER=0.
	if os.Getenv("WEBKIT_DISABLE_DMABUF_RENDERER") == "" {
		_ = os.Setenv("WEBKIT_DISABLE_DMABUF_RENDERER", "1")
	}

	a, err := app.New()
	if err != nil {
		log.Fatal(err)
	}

	assetsHandler := application.AssetFileServerFS(assets)
	wailsApp := application.New(application.Options{
		Name:        "shelve",
		Description: "Lightweight local SSH session manager",
		Icon:        appIcon,
		Assets: application.AssetOptions{
			// Plan P005: /termws-port provisions the loopback terminal
			// WebSocket address to the frontend (the webview loads from the
			// wails:// custom scheme, which cannot carry WebSockets, so the
			// socket lives on a dedicated 127.0.0.1 listener started by the
			// app). /terminal is also delegated for tests/back-compat;
			// everything else serves the embedded frontend as before.
			Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/terminal", "/termws-port":
					a.TerminalWS().ServeHTTP(w, r)
					return
				}
				assetsHandler.ServeHTTP(w, r)
			}),
		},
		Services: []application.Service{
			application.NewService(a.AppService()),
			application.NewService(a.VaultService()),
			application.NewService(a.SessionService()),
			application.NewService(a.CredentialService()),
			application.NewService(a.JumpHostService()),
			application.NewService(a.TerminalService()),
			application.NewService(a.SftpService()),
			application.NewService(a.MonitorService()),
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
		OnShutdown: a.Shutdown,
	})

	// Wire Go→JS events now that the runtime exists (master plan §5).
	a.SetEmitter(wailsvc.FuncEmitter(func(event string, payload any) {
		wailsApp.Event.Emit(event, payload)
	}))

	wailsApp.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:             "main",
		Title:            "Shelve",
		Width:            1280,
		Height:           800,
		MinWidth:         960,
		MinHeight:        540,
		BackgroundColour: application.NewRGB(6, 7, 15),
		URL:              "/",
	})

	if err := wailsApp.Run(); err != nil {
		log.Fatal(err)
	}
}
