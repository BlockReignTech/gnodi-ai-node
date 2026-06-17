// Command gnodi-ai-node is the operator daemon for the Gnodi AI Node Network.
// It serves an OpenAI-compatible inference endpoint backed by a local engine
// (Ollama/vLLM), registers with NodeSvc, reports its capabilities, and heartbeats.
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

	if err := daemon.New(cfg).Run(ctx); err != nil {
		log.Fatalf("daemon exited with error: %v", err)
	}
}
