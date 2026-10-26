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

// VramBudget bounds how much card memory the warmed set may occupy.
//
// It exists because the catalog's VRAM threshold is per model and not
// cumulative. Every model at or under the card's size is eligible, and with
// weights pinned resident the node holds all of them at once — so a 15GB card
// selects three models totalling about 20GB. That does not fail cleanly: the
// engine pages weights in and out on every request, and from the gateway's side
// the node simply becomes slow and unreliable.
//
// A technical operator works around it by listing MODELS by hand. Nobody
// installing a desktop app will, which is what makes this a correctness problem
// rather than a tuning one.
type VramBudget struct {
	// Bytes the resident set may occupy. Zero means unbounded.
	Bytes int64
	// Resident reports what the engine currently holds, in bytes.
	Resident func(context.Context) (int64, error)
	// Unload releases one engine reference from card memory.
	Unload func(context.Context, string) error
}

func (b *VramBudget) enforced() bool {
	return b != nil && b.Bytes > 0 && b.Resident != nil
}

// Warm loads every catalogued model into the engine before the node offers to
// serve it, and drops any that will not load or will not fit.
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
func (p *Proxy) Warm(ctx context.Context, budget *VramBudget) {
	if budget.enforced() {
		log.Printf("warm: holding the resident set under %s of VRAM", gib(budget.Bytes))
	}
	for _, id := range p.warmOrder() {
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

		// Measured after loading rather than predicted before it. A model's
		// resident footprint is not its file size and not the catalog's
		// threshold — on a T4 two models whose thresholds sum to 16GB occupy
		// 10.2GB in fact. Asking the engine what it is holding is the only
		// number that is actually true on this card.
		if budget.enforced() {
			used, err := budget.Resident(ctx)
			switch {
			case err != nil:
				// A failed probe is not the model's fault; keep it rather than
				// punish it for the engine being briefly unreachable.
				log.Printf("warm: could not read resident VRAM, keeping %s: %v", id, err)
			case used > budget.Bytes:
				log.Printf("warm: dropping %s — the resident set would reach %s, past the %s budget",
					id, gib(used), gib(budget.Bytes))
				if budget.Unload != nil {
					if err := budget.Unload(ctx, ref); err != nil {
						log.Printf("warm: could not unload %s: %v", ref, err)
					}
				}
				p.drop(id)
				// Deliberately no break. The order is largest first, so a model
				// that does not fit says nothing about the smaller ones behind
				// it — one of those may be exactly what this card can hold.
				continue
			}
		}

		log.Printf("warm: %s ready in %s", id, time.Since(started).Round(time.Millisecond))
	}

	if len(p.Models()) == 0 {
		log.Printf("warm: nothing is being served — no catalog model both loads and fits this card")
	}
}

func gib(b int64) string {
	return fmt.Sprintf("%.1fGiB", float64(b)/(1<<30))
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
