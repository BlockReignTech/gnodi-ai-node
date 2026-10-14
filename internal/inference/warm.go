package inference

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

// warmTimeout bounds one model's first load.
//
// Loading weights into VRAM is slow and highly variable: measured at 115s for
// an 8B model on a T4, against ~45ms once resident. This is set well past the
// worst observed load so a slow card is warmed rather than disqualified.
const warmTimeout = 5 * time.Minute

// Warm loads every catalogued model into the engine before the node offers to
// serve it, and drops any that will not load.
//
// A node used to advertise whatever weights were on disk, then load them on the
// first real request. That first request took the better part of two minutes —
// long past the gateway's job timeout — so it failed over to the commercial
// fallback, and with a concurrency of one it blocked every request behind it.
// From a user's seat the node simply hung.
//
// Warming through the same /chat/completions path a job takes is deliberate: it
// proves the whole route works, not merely that a file exists on disk. A model
// that cannot be served is removed from the catalog, so the node advertises
// only what it can actually deliver — an honest, smaller catalog beats a
// generous one that times out.
func (p *Proxy) Warm(ctx context.Context) {
	for _, id := range p.Models() {
		ref, ok := p.ref(id)
		if !ok {
			continue
		}
		started := time.Now()
		if err := p.warmOne(ctx, ref); err != nil {
			log.Printf("warm: dropping %s — it did not load: %v", id, err)
			p.drop(id)
			continue
		}
		log.Printf("warm: %s ready in %s", id, time.Since(started).Round(time.Millisecond))
	}
}

func (p *Proxy) warmOne(ctx context.Context, ref string) error {
	ctx, cancel := context.WithTimeout(ctx, warmTimeout)
	defer cancel()

	// One token is enough to force the load; asking for more only burns time.
	// `keep_alive` is Ollama-specific and ignored elsewhere — without it the
	// engine may evict the model again before the first job arrives, which
	// would make this whole exercise pointless.
	body, err := json.Marshal(map[string]any{
		"model":      ref,
		"messages":   []map[string]string{{"role": "user", "content": "hi"}},
		"max_tokens": 1,
		"keep_alive": -1,
	})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.backendURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// Drain so the connection can be reused for the next model.
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("engine returned %d", resp.StatusCode)
	}
	return nil
}

// drop removes a model from the catalog, so it is neither advertised nor served.
func (p *Proxy) drop(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.catalog, id)
}
