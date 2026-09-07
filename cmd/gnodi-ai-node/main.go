// Command gnodi-ai-node is the operator daemon for the Gnodi AI Node Network.
// It dials the gateway, serves inference jobs pushed down that socket from a
// local engine (Ollama/vLLM), registers and heartbeats with NodeSvc, and
// exposes a loopback status page. It listens for no inbound traffic.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/blockreigntech/gnodi-ai-node/internal/config"
	"github.com/blockreigntech/gnodi-ai-node/internal/daemon"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config error: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	d, err := daemon.New(cfg)
	if err != nil {
		log.Fatalf("startup error: %v", err)
	}
	if err := d.Run(ctx); err != nil {
		log.Fatalf("daemon exited with error: %v", err)
	}
}
