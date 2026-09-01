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
	// Software-only rendering (WEBKIT_DISABLE_DMABUF_RENDERER=1) was the
	// original default (master plan §11, README "Troubleshooting") as a
	// blank-window workaround for GPU-problematic setups (NVIDIA/gbm,
	// Wayland sessions). But it breaks canvas presentation when the window
	// is moved between monitors with different scale factors on X11 — the
	// terminal freezes (no repaints) until the window returns to the
	// original screen (verified: enabling dmabuf removes the freeze).
	//
	// Fix: default to the GPU dmabuf renderer everywhere EXCEPT explicit
	// Wayland sessions (the case the original workaround targeted). Users
	// can still force either side via WEBKIT_DISABLE_DMABUF_RENDERER=0/1.
	if os.Getenv("XDG_SESSION_TYPE") == "wayland" &&
		os.Getenv("WEBKIT_DISABLE_DMABUF_RENDERER") == "" {
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
