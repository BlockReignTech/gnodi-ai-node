// Package inference exposes an OpenAI-compatible /v1/chat/completions endpoint
// that forwards to a local backend (Ollama, vLLM, …), enforcing the node's model
// allowlist. The gateway routes inference requests here.
package inference

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"sync"
)

// Proxy forwards chat-completion requests to an OpenAI-compatible backend.
//
// It holds a catalog mapping the network's model ids to engine references: the
// gateway routes on `gnodi/llama3.1-8b`, while Ollama knows the same weights as
// `llama3.1:8b-instruct-q4_K_M`. Translating here keeps the engine's naming out
// of the protocol.
type Proxy struct {
	backendURL string // POSTs to backendURL + "/chat/completions"
	mu         sync.RWMutex
	catalog    map[string]string // model id -> engine ref
	order      []string          // catalog ids, largest model first
	http       *http.Client
}

// NewProxy builds a proxy serving exactly `models`, where each id is also its
// engine reference. The manifest replaces this mapping via SetCatalog.
func NewProxy(backendURL string, models []string, hc *http.Client) *Proxy {
	if hc == nil {
		hc = http.DefaultClient
	}
	catalog := make(map[string]string, len(models))
	for _, m := range models {
		catalog[m] = m
	}
	return &Proxy{backendURL: backendURL, catalog: catalog, http: hc}
}

// SetCatalog replaces the served models. Safe to call while jobs are running.
//
// `order` decides which models get the card's memory when they cannot all be
// held at once; an empty order falls back to whatever order the map yields,
// which is fine when nothing is competing for memory.
func (p *Proxy) SetCatalog(catalog map[string]string, order ...string) {
	next := make(map[string]string, len(catalog))
	for id, ref := range catalog {
		next[id] = ref
	}
	seq := make([]string, 0, len(next))
	for _, id := range order {
		if _, ok := next[id]; ok {
			seq = append(seq, id)
		}
	}
	// Anything the caller did not rank still has to be warmed.
	for _, id := range sortedKeys(next) {
		if !contains(seq, id) {
			seq = append(seq, id)
		}
	}
	p.mu.Lock()
	p.catalog, p.order = next, seq
	p.mu.Unlock()
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// warmOrder is the catalog in the order memory should be given out.
func (p *Proxy) warmOrder() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return append([]string(nil), p.order...)
}

// ref resolves a network model id to its engine reference.
func (p *Proxy) ref(id string) (string, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	r, ok := p.catalog[id]
	return r, ok
}

// Models returns the served model ids, sorted for stable reporting.
func (p *Proxy) Models() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]string, 0, len(p.catalog))
	for m := range p.catalog {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

// ChatCompletions handles POST /v1/chat/completions. It checks the requested
// model is served, forwards the request body to the backend, and streams the
// response back unchanged (works for both streaming and non-streaming).
func (p *Proxy) ChatCompletions(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1 MiB cap
	if err != nil {
		writeError(w, http.StatusBadRequest, "could not read request body")
		return
	}

	var probe struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &probe); err != nil || probe.Model == "" {
		writeError(w, http.StatusBadRequest, "request must include a JSON \"model\" field")
		return
	}
	engineRef, ok := p.ref(probe.Model)
	if !ok {
		writeError(w, http.StatusNotFound, "model not served by this node: "+probe.Model)
		return
	}

	// The caller asks for a network id; the engine wants its own reference.
	// Forwarding the body unchanged sent Ollama "gnodi/llama3.1-8b", a name it
	// has never heard of, and it answered with an error object that this
	// handler passed straight back. The job path has always translated here
	// (see Stream); this one did not, and nothing was mounted on it to notice.
	body, err = withModel(body, engineRef)
	if err != nil {
		writeError(w, http.StatusBadRequest, "could not rewrite the model field: "+err.Error())
		return
	}

	if err := p.forward(r.Context(), body, w); err != nil {
		writeError(w, http.StatusBadGateway, "inference backend error: "+err.Error())
	}
}

func (p *Proxy) forward(ctx context.Context, body []byte, w http.ResponseWriter) error {
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

	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(resp.StatusCode)
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 4096)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return nil // client gone
			}
			if flusher != nil {
				flusher.Flush() // stream SSE chunks promptly
			}
		}
		if rerr == io.EOF {
			return nil
		}
		if rerr != nil {
			return nil // backend stream ended/errored; response already started
		}
	}
}

func writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": msg}})
}

// withModel replaces the "model" field, leaving every other field byte-exact.
//
// Decoded into raw messages rather than a struct so that whatever else the
// caller sent — temperature, stream, tools, anything this daemon has no opinion
// about — reaches the engine exactly as written.
func withModel(body []byte, model string) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, err
	}
	ref, err := json.Marshal(model)
	if err != nil {
		return nil, err
	}
	fields["model"] = ref
	return json.Marshal(fields)
}
