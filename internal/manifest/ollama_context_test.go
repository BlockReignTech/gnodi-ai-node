package manifest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The engine's reported context window is the only way to find out what a node
// is really serving. It went unread, and the whole fleet served Ollama's 4096
// default while the catalog advertised 32768.
func TestResident_ReadsContextLength(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/ps" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"models":[
			{"name":"qwen3.5:9b-q4_K_M","size_vram":6113858682,"context_length":16384},
			{"name":"gemma3:12b-it-q4_K_M","size_vram":8697308774}
		]}`))
	}))
	defer srv.Close()

	loaded, err := NewOllama(srv.URL, srv.Client()).Resident(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 2 {
		t.Fatalf("got %d models, want 2", len(loaded))
	}
	if loaded[0].ContextTokens != 16384 {
		t.Errorf("context = %d, want 16384", loaded[0].ContextTokens)
	}
	if loaded[0].VramBytes != 6113858682 {
		t.Errorf("vram = %d", loaded[0].VramBytes)
	}
	// An engine too old to report it says 0 rather than guessing, so the
	// check can tell "not reported" apart from "reported as small".
	if loaded[1].ContextTokens != 0 {
		t.Errorf("absent context = %d, want 0", loaded[1].ContextTokens)
	}
}
