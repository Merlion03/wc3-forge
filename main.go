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
	var startReforged bool
	flag.BoolVar(&headless, "headless", false, "run the bridge only; do not open the GUI window")
	flag.BoolVar(&noBridge, "no-bridge", false, "skip starting the MCP bridge (for GUI-only dev)")
	flag.StringVar(&openPath, "open", "", "extracted map folder to load on startup (skips Open Map dialog)")
	flag.BoolVar(&startReforged, "reforged", false, "start in HD (Reforged) graphics mode; default is SD (Classic)")
	flag.Parse()

	if startReforged {
		reforgedMode.Store(true)
	}

	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.SetPrefix("wc3-forge: ")
	// GUI Wails apps have no console; route Go logs to a file alongside
	// the binary so asset-handler diagnostics are visible.
	if logFile, err := os.OpenFile("wc3-forge.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
		log.SetOutput(logFile)
	}

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

	// Open CASC eagerly so the viewer's first asset requests (which fire
	// during loadBaseFiles, immediately after the JS bundle initializes)
	// land on a ready storage. Lazy init introduces a race: the viewer's
	// loadBaseFiles can complete with errors before CASC finishes opening,
	// and the viewer doesn't retry on its own.
	if c, err := getCASC(); err != nil {
		log.Printf("CASC eager open failed (stock asset paths will 404): %v", err)
	} else if c != nil {
		// Sync the just-opened storage to the current reforged-mode atomic
		// (always false on startup; honored here in case --reforged or
		// similar persistent state lands later).
		c.SetReforged(ReforgedMode())
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
