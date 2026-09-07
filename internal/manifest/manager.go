package manifest

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sort"
)

// Catalog maps a network model id to the engine reference that serves it.
// The gateway routes on ids; the node translates when calling the engine.
type Catalog map[string]string

// Manager keeps the local engine in step with the signed catalog.
type Manager struct {
	URL       string // gateway manifest endpoint
	PublicKey string // pinned, not fetched
	VramGb    int
	Allow     []string // optional operator allowlist of model ids
	AutoPull  bool
	HTTP      *http.Client
	Ollama    *Ollama
	Logger    *log.Logger
}

// SyncResult reports what a sync settled on.
type SyncResult struct {
	Version int
	Catalog Catalog
	Pulled  []string
	Skipped map[string]string // model id -> why
}

// Sync fetches the catalog, pulls what this machine can run, and returns the
// models that are actually ready to serve.
//
// A model whose local digest does not match the manifest is **not served**: the
// node holds different weights than the network promised, and serving them
// under the network's model id is exactly the quality drift pinning exists to
// prevent.
func (m *Manager) Sync(ctx context.Context) (SyncResult, error) {
	res := SyncResult{Catalog: Catalog{}, Skipped: map[string]string{}}

	hc := m.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	manifest, err := Fetch(ctx, hc, m.URL, m.PublicKey)
	if err != nil {
		return res, err
	}
	res.Version = manifest.Version

	wanted := SelectForVram(manifest, m.VramGb, m.Allow)
	if len(wanted) == 0 {
		return res, fmt.Errorf("no catalog model fits %dGB of VRAM", m.VramGb)
	}

	local, err := m.Ollama.Local(ctx)
	if err != nil {
		return res, err
	}

	for _, model := range wanted {
		have, present := local[model.Ref]

		if !present {
			if !m.AutoPull {
				res.Skipped[model.ID] = "not installed and auto-pull is off"
				continue
			}
			m.logf("manifest: pulling %s (%s, %dGB+)", model.ID, model.Ref, model.MinVramGb)
			if err := m.Ollama.Pull(ctx, model.Ref, func(status string) {
				m.logf("manifest:   %s — %s", model.ID, status)
			}); err != nil {
				res.Skipped[model.ID] = err.Error()
				continue
			}
			refreshed, err := m.Ollama.Local(ctx)
			if err != nil {
				return res, err
			}
			local = refreshed
			have, present = local[model.Ref]
			if present {
				res.Pulled = append(res.Pulled, model.ID)
			}
		}

		switch {
		case !present:
			res.Skipped[model.ID] = "still missing after pull"
		case have != model.Digest:
			// Loud, because it means the operator is holding weights the
			// network did not publish under this id.
			m.logf("manifest: REFUSING %s — local digest %s does not match the pinned %s",
				model.ID, short(have), short(model.Digest))
			res.Skipped[model.ID] = "digest mismatch"
		default:
			res.Catalog[model.ID] = model.Ref
		}
	}

	if len(res.Catalog) == 0 {
		return res, fmt.Errorf("no catalog model is ready to serve")
	}
	m.logf("manifest: v%d ready with %s", res.Version, join(res.Catalog))
	return res, nil
}

func (m *Manager) logf(format string, args ...any) {
	if m.Logger != nil {
		m.Logger.Printf(format, args...)
	}
}

func short(digest string) string {
	if len(digest) > 19 {
		return digest[:19] + "…"
	}
	return digest
}

func join(c Catalog) string {
	ids := make([]string, 0, len(c))
	for id := range c {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := ""
	for i, id := range ids {
		if i > 0 {
			out += ", "
		}
		out += id
	}
	return out
}
