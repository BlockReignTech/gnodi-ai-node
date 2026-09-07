package inference

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// sseBackend serves a canned SSE stream, as Ollama or vLLM would.
func sseBackend(t *testing.T, events []string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		for _, e := range events {
			fmt.Fprintf(w, "data: %s\n\n", e)
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func delta(s string) string {
	return `{"choices":[{"delta":{"content":"` + s + `"}}]}`
}

func TestStream_YieldsDeltasAndUsage(t *testing.T) {
	be := sseBackend(t, []string{
		delta("Hel"), delta("lo"),
		`{"choices":[{"delta":{}}],"usage":{"prompt_tokens":3,"completion_tokens":9}}`,
		"[DONE]",
	})
	p := NewProxy(be.URL, []string{"m"}, nil)

	var got []string
	res, err := p.Stream(context.Background(), "m",
		[]Message{{Role: "user", Content: "hi"}}, 32,
		func(d string) error { got = append(got, d); return nil })
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if strings.Join(got, "") != "Hello" {
		t.Errorf("deltas = %v", got)
	}
	if res.PromptTokens != 3 || res.CompletionTokens != 9 {
		t.Errorf("usage = %+v", res)
	}
}

func TestStream_SkipsUnparseableFrames(t *testing.T) {
	// Backends emit keep-alives and vendor events we have no interest in;
	// those must not abort a working stream.
	be := sseBackend(t, []string{"not json", `{"unrelated":true}`, delta("ok"), "[DONE]"})
	p := NewProxy(be.URL, []string{"m"}, nil)

	var got []string
	if _, err := p.Stream(context.Background(), "m",
		[]Message{{Role: "user", Content: "hi"}}, 8,
		func(d string) error { got = append(got, d); return nil }); err != nil {
		t.Fatalf("stream: %v", err)
	}
	if strings.Join(got, "") != "ok" {
		t.Errorf("deltas = %v", got)
	}
}

func TestStream_RejectsUnservedModel(t *testing.T) {
	p := NewProxy("http://unused", []string{"m"}, nil)
	_, err := p.Stream(context.Background(), "other",
		[]Message{{Role: "user", Content: "hi"}}, 8, func(string) error { return nil })
	if !errors.Is(err, ErrModelNotServed) {
		t.Fatalf("got %v, want ErrModelNotServed", err)
	}
}

func TestStream_PropagatesBackendFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	p := NewProxy(srv.URL, []string{"m"}, nil)

	if _, err := p.Stream(context.Background(), "m",
		[]Message{{Role: "user", Content: "hi"}}, 8, func(string) error { return nil }); err == nil {
		t.Fatal("expected an error for a 500 from the backend")
	}
}

func TestStream_StopsWhenTheConsumerFails(t *testing.T) {
	be := sseBackend(t, []string{delta("a"), delta("b"), delta("c"), "[DONE]"})
	p := NewProxy(be.URL, []string{"m"}, nil)

	count := 0
	_, err := p.Stream(context.Background(), "m",
		[]Message{{Role: "user", Content: "hi"}}, 8,
		func(string) error { count++; return errors.New("socket gone") })
	if err == nil {
		t.Fatal("expected the consumer error to surface")
	}
	if count != 1 {
		t.Errorf("kept generating after the consumer failed: %d deltas", count)
	}
}

func TestServes(t *testing.T) {
	p := NewProxy("http://x", []string{"a", "b"}, nil)
	if !p.Serves("a") || p.Serves("c") {
		t.Error("Serves does not match the allowlist")
	}
}
