// Command shelve-backend is the standalone Shelve backend (master plan §5).
//
// It builds the composition root, serves the api services over the
// token-gated loopback bridge, prints the one-line JSON handshake on stdout
// for the Electron main process to parse, and shuts down on SIGINT/SIGTERM.
// Logs go to stderr so stdout stays reserved for the handshake.
package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"shelve/internal/app"
	"shelve/internal/bridge"
)

func main() {
	log.SetOutput(os.Stderr)

	a, err := app.New()
	if err != nil {
		log.Fatal(err)
	}

	srv := bridge.New(a.TerminalWS())
	srv.RegisterAll(a)

	// Go→JS events flow through the bridge now that it exists; the services
	// were built earlier over a LateEmitter.
	a.SetEmitter(srv)

	if _, err := srv.Start(); err != nil {
		log.Fatal(err)
	}
	// The one stdout line: {"event":"ready","addr":"127.0.0.1:PORT","token":"…"}.
	fmt.Println(srv.Handshake())

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	// Shutdown order (master plan §5): drop the transport first so blocked
	// output writers release, then tear down the engine/sftp/monitor/store.
	_ = srv.Close()
	a.Shutdown()
}
