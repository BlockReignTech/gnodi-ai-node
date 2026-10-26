package inference

import (
	"context"
	"slices"
	"testing"

	"github.com/blockreigntech/gnodi-ai-node/internal/manifest"
)

// Measured resident sizes (q4_K_M, from `ollama ps` on the reference node and
// from on-disk size + observed KV overhead for the rest).
var residentMiB = map[string]int64{
	"gnodi/llama3.1-8b":  5300,
	"gnodi/mistral-7b":   4900,
	"gnodi/gemma3-12b":   8700,
	"gnodi/qwen2.5-14b":  9400,
	"gnodi/gemma4-31b":   18500,
	"gnodi/qwen2.5-32b":  20000,
	"gnodi/llama3.1-70b": 40000,
}

// Catalog thresholds, largest first, as SelectForVram orders them.
var byThreshold = []struct {
	id   string
	vram int
}{
	{"gnodi/llama3.1-70b", 48}, {"gnodi/qwen2.5-32b", 24}, {"gnodi/gemma4-31b", 24},
	{"gnodi/qwen2.5-14b", 16}, {"gnodi/gemma3-12b", 12},
	{"gnodi/llama3.1-8b", 8}, {"gnodi/mistral-7b", 8},
}

// What each real card ends up serving, asserted rather than logged, because the
// fill policy is a product decision and a silent change to it would quietly
// strand operators on models the router does not ask for.
func TestWhatEachCardServes(t *testing.T) {
	want := map[int][]string{
		8:  {"gnodi/mistral-7b"},
		12: {"gnodi/llama3.1-8b", "gnodi/mistral-7b"},
		16: {"gnodi/llama3.1-8b", "gnodi/mistral-7b"},
		24: {"gnodi/gemma3-12b", "gnodi/llama3.1-8b", "gnodi/mistral-7b"},
	}
	for _, card := range []int{8, 12, 16, 24} {
		var order []string
		catalog := map[string]string{}
		sizes := map[string]int64{}
		for i := len(byThreshold) - 1; i >= 0; i-- {
			m := byThreshold[i]
			if m.vram <= card {
				ref := m.id + ":ref"
				order = append(order, m.id)
				catalog[m.id] = ref
				sizes[ref] = residentMiB[m.id] * (1 << 20)
			}
		}
		e := newEngine(sizes)
		srv := e.serve()
		p := NewProxy(srv.URL, nil, nil)
		p.SetCatalog(catalog, order...)
		budget := manifest.BudgetBytes(card)
		p.Warm(context.Background(), &VramBudget{Bytes: budget, Resident: e.resident, Unload: e.unload})
		used, _ := e.resident(context.Background())
		srv.Close()

		got := p.Models()
		t.Logf("%2dGB card | eligible %d | budget %.1fGiB | serves %v (%.1fGiB used)",
			card, len(order), float64(budget)/(1<<30), got, float64(used)/(1<<30))
		if !slices.Equal(got, want[card]) {
			t.Errorf("%dGB card: serves %v, expected %v", card, got, want[card])
		}
		if used > budget {
			t.Errorf("%dGB card: resident %.1fGiB exceeds the %.1fGiB budget",
				card, float64(used)/(1<<30), float64(budget)/(1<<30))
		}
	}
}
