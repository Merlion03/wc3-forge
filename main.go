package main

import (
	"embed"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/StephenSHorton/wc3-forge/internal/bridge"
	"github.com/StephenSHorton/wc3-forge/internal/forge"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	var headless bool
	var noBridge bool
	var openPath string
	flag.BoolVar(&headless, "headless", false, "run the bridge only; do not open the GUI window")
	flag.BoolVar(&noBridge, "no-bridge", false, "skip starting the MCP bridge (for GUI-only dev)")
	flag.StringVar(&openPath, "open", "", "extracted map folder to load on startup (skips Open Map dialog)")
	flag.Parse()

	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.SetPrefix("wc3-forge: ")

	// Bridge always runs unless explicitly disabled. The GUI and external MCP
	// clients can coexist — both read/write the same forge.Session.
	var b *bridge.Bridge
	if !noBridge {
		b = bridge.New()
		forge.RegisterAll(b)
		if err := b.Start(); err != nil {
			log.Fatalf("bridge start failed: %v", err)
		}
		defer b.Stop()
		fmt.Printf("wc3-forge: bridge on 127.0.0.1:%d (pid %d)\n", b.Port(), os.Getpid())
	}

	if openPath != "" {
		if err := forge.Current.Open(openPath); err != nil {
			log.Printf("--open %q failed: %v", openPath, err)
		} else {
			log.Printf("--open %q OK", openPath)
		}
	}

	if headless {
		// Block on signals; no GUI to drive the event loop.
		sigs := make(chan os.Signal, 1)
		signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
		<-sigs
		fmt.Println("wc3-forge: shutting down")
		return
	}

	// GUI mode. wails.Run blocks until the window closes.
	app := NewApp()
	err := wails.Run(&options.App{
		Title:  "wc3-forge",
		Width:  1280,
		Height: 800,
		AssetServer: &assetserver.Options{
			Assets:  assets,
			Handler: newAssetHandler(),
		},
		BackgroundColour: &options.RGBA{R: 18, G: 18, B: 20, A: 1},
		OnStartup:        app.startup,
		Bind: []interface{}{
			app,
		},
	})
	if err != nil {
		log.Fatalf("wails.Run: %v", err)
	}
}
