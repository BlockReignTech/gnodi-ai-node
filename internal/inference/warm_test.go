package inference

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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

	p.Warm(context.Background())

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

	p.Warm(context.Background())

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

	p.Warm(context.Background())

	if got := p.Models(); len(got) != 0 {
		t.Fatalf("expected nothing advertised when the engine is unreachable, got %v", got)
	}
}
