package inference

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
)

// What matters about warming is not that it runs, but what the node advertises
// afterwards: a model that will not load must not be offered to the gateway.

func TestWarmLoadsEveryModelBeforeAdvertising(t *testing.T) {
	var mu sync.Mutex
	var asked []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("warm should use the same path a job takes, got %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		mu.Lock()
		asked = append(asked, req["model"].(string))
		mu.Unlock()
		// One token is enough to force a load; asking for more only burns time.
		if n, ok := req["max_tokens"].(float64); !ok || n != 1 {
			t.Errorf("expected max_tokens 1, got %v", req["max_tokens"])
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"x"}}]}`))
	}))
	defer srv.Close()

	p := NewProxy(srv.URL, nil, nil)
	p.SetCatalog(map[string]string{"gnodi/a": "a:ref", "gnodi/b": "b:ref"})

	p.Warm(context.Background(), nil)

	mu.Lock()
	defer mu.Unlock()
	if len(asked) != 2 {
		t.Fatalf("expected both models loaded, got %v", asked)
	}
	if got := p.Models(); len(got) != 2 {
		t.Fatalf("both models load, so both stay advertised; got %v", got)
	}
}

func TestWarmDropsAModelThatWillNotLoad(t *testing.T) {
	// The whole point: a node that advertises weights it cannot serve has the
	// gateway route real traffic into a timeout, which fails over to the paid
	// fallback and looks to the user like a hang.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		if req["model"] == "broken:ref" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"x"}}]}`))
	}))
	defer srv.Close()

	p := NewProxy(srv.URL, nil, nil)
	p.SetCatalog(map[string]string{"gnodi/good": "good:ref", "gnodi/broken": "broken:ref"})

	p.Warm(context.Background(), nil)

	got := p.Models()
	if len(got) != 1 || got[0] != "gnodi/good" {
		t.Fatalf("expected only the loadable model advertised, got %v", got)
	}
	if p.Serves("gnodi/broken") {
		t.Fatal("a dropped model must not be servable either")
	}
}

func TestWarmSurvivesAnUnreachableEngine(t *testing.T) {
	// If the engine is down at boot the node should advertise nothing rather
	// than crash — the gateway then routes elsewhere instead of to a black hole.
	p := NewProxy("http://127.0.0.1:1", nil, nil)
	p.SetCatalog(map[string]string{"gnodi/a": "a:ref"})

	p.Warm(context.Background(), nil)

	if got := p.Models(); len(got) != 0 {
		t.Fatalf("expected nothing advertised when the engine is unreachable, got %v", got)
	}
}

// The failure these cover: the catalog's VRAM threshold is per model, not
// cumulative, and pinned weights are held all at once. A 15GB card therefore
// selects three models totalling ~20GB, which thrashes rather than fails.

// engine stands in for Ollama: it loads anything, and reports a resident total
// that grows by a fixed amount per distinct model held.
type engine struct {
	mu       sync.Mutex
	loaded   map[string]int64
	sizes    map[string]int64
	unloaded []string
}

func newEngine(sizes map[string]int64) *engine {
	return &engine{loaded: map[string]int64{}, sizes: sizes}
}

func (e *engine) serve() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		ref, _ := req["model"].(string)
		e.mu.Lock()
		e.loaded[ref] = e.sizes[ref]
		e.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"x"}}]}`))
	}))
}

func (e *engine) resident(context.Context) (int64, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	var total int64
	for _, s := range e.loaded {
		total += s
	}
	return total, nil
}

func (e *engine) unload(_ context.Context, ref string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.loaded, ref)
	e.unloaded = append(e.unloaded, ref)
	return nil
}

const gb = int64(1) << 30

func TestWarmStopsAtTheVramBudget(t *testing.T) {
	// Three models whose thresholds all clear a 15GB card, and which together
	// need 20GB of it.
	e := newEngine(map[string]int64{"big:ref": 9 * gb, "mid:ref": 7 * gb, "small:ref": 4 * gb})
	srv := e.serve()
	defer srv.Close()

	p := NewProxy(srv.URL, nil, nil)
	p.SetCatalog(
		map[string]string{"gnodi/big": "big:ref", "gnodi/mid": "mid:ref", "gnodi/small": "small:ref"},
		"gnodi/big", "gnodi/mid", "gnodi/small",
	)

	p.Warm(context.Background(), &VramBudget{Bytes: 13 * gb, Resident: e.resident, Unload: e.unload})

	// big (9) fits. mid would reach 16, past 13, so it is dropped and unloaded.
	// small (4) brings the set to 13, which still fits — so it is kept, because
	// a model that does not fit says nothing about the smaller ones behind it.
	got := p.Models()
	if len(got) != 2 || !slices.Contains(got, "gnodi/big") || !slices.Contains(got, "gnodi/small") {
		t.Fatalf("expected big and small to be served, got %v", got)
	}
	if !slices.Contains(e.unloaded, "mid:ref") {
		t.Fatalf("the model that did not fit should have been released, unloaded=%v", e.unloaded)
	}
}

func TestWarmServesEverythingThatFits(t *testing.T) {
	// The reference node: two models whose thresholds sum to 16GB but which
	// occupy 10.2GB in fact. Measuring rather than predicting is what keeps
	// this pair working.
	e := newEngine(map[string]int64{"a:ref": 5300 * (1 << 20), "b:ref": 4900 * (1 << 20)})
	srv := e.serve()
	defer srv.Close()

	p := NewProxy(srv.URL, nil, nil)
	p.SetCatalog(map[string]string{"gnodi/a": "a:ref", "gnodi/b": "b:ref"}, "gnodi/a", "gnodi/b")

	p.Warm(context.Background(), &VramBudget{Bytes: 12800 * (1 << 20), Resident: e.resident, Unload: e.unload})

	if got := p.Models(); len(got) != 2 {
		t.Fatalf("both fit inside the budget and should be served, got %v", got)
	}
	if len(e.unloaded) != 0 {
		t.Fatalf("nothing should have been unloaded, got %v", e.unloaded)
	}
}

func TestWarmKeepsAModelWhenTheProbeFails(t *testing.T) {
	// A model is not at fault for the engine being briefly unreachable, and
	// dropping it would cost the operator a model for someone else's outage.
	e := newEngine(map[string]int64{"a:ref": gb})
	srv := e.serve()
	defer srv.Close()

	p := NewProxy(srv.URL, nil, nil)
	p.SetCatalog(map[string]string{"gnodi/a": "a:ref"}, "gnodi/a")

	p.Warm(context.Background(), &VramBudget{
		Bytes:    gb,
		Resident: func(context.Context) (int64, error) { return 0, errors.New("engine unreachable") },
		Unload:   e.unload,
	})

	if got := p.Models(); len(got) != 1 {
		t.Fatalf("expected the model to be kept, got %v", got)
	}
}

func TestWarmHonoursTheGivenOrder(t *testing.T) {
	// Which models get the memory is a decision, not an accident of map
	// iteration or alphabetical order: "small" sorts first but must not take
	// the budget from the larger model the node is more useful for serving.
	e := newEngine(map[string]int64{"big:ref": 8 * gb, "small:ref": 8 * gb})
	srv := e.serve()
	defer srv.Close()

	p := NewProxy(srv.URL, nil, nil)
	p.SetCatalog(
		map[string]string{"gnodi/small": "small:ref", "gnodi/zbig": "big:ref"},
		"gnodi/zbig", "gnodi/small",
	)

	p.Warm(context.Background(), &VramBudget{Bytes: 10 * gb, Resident: e.resident, Unload: e.unload})

	got := p.Models()
	if len(got) != 1 || got[0] != "gnodi/zbig" {
		t.Fatalf("expected the ranked-first model to win the budget, got %v", got)
	}
}

func TestWarmWithoutABudgetServesEverything(t *testing.T) {
	// An operator who listed MODELS by hand has already made this decision.
	e := newEngine(map[string]int64{"a:ref": 99 * gb, "b:ref": 99 * gb})
	srv := e.serve()
	defer srv.Close()

	p := NewProxy(srv.URL, nil, nil)
	p.SetCatalog(map[string]string{"gnodi/a": "a:ref", "gnodi/b": "b:ref"}, "gnodi/a", "gnodi/b")

	p.Warm(context.Background(), nil)

	if got := p.Models(); len(got) != 2 {
		t.Fatalf("expected both served with no budget, got %v", got)
	}
}
