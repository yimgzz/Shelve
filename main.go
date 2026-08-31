package main

import (
	"embed"
	"log"

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
	a, err := app.New()
	if err != nil {
		log.Fatal(err)
	}

	wailsApp := application.New(application.Options{
		Name:        "shelve",
		Description: "Lightweight local SSH session manager",
		Icon:        appIcon,
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Services: []application.Service{
			application.NewService(a.AppService()),
			application.NewService(a.VaultService()),
			application.NewService(a.SessionService()),
			application.NewService(a.TerminalService()),
			application.NewService(a.SftpService()),
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
