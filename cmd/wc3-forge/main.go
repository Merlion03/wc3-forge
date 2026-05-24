package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/StephenSHorton/wc3-forge/internal/bridge"
)

func main() {
	var noBridge bool
	flag.BoolVar(&noBridge, "no-bridge", false, "skip starting the MCP bridge")
	flag.Parse()

	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.SetPrefix("wc3-forge: ")

	if noBridge {
		log.Println("bridge disabled by --no-bridge; nothing to do")
		return
	}

	b := bridge.New()
	bridge.RegisterDefaults(b)

	if err := b.Start(); err != nil {
		log.Fatalf("bridge start failed: %v", err)
	}
	defer b.Stop()

	fmt.Printf("wc3-forge: listening on 127.0.0.1:%d (pid %d)\n", b.Port(), os.Getpid())

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	<-sigs
	fmt.Println("wc3-forge: shutting down")
}
