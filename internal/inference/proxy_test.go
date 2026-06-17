package inference

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func backend(t *testing.T) (*httptest.Server, *string) {
	t.Helper()
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("backend path = %q", r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"hi"}}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &gotBody
}

func post(p *Proxy, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	p.ChatCompletions(rec, req)
	return rec
}

func TestProxy_ForwardsAllowedModel(t *testing.T) {
	srv, gotBody := backend(t)
	p := NewProxy(srv.URL, []string{"llama2-13b"}, srv.Client())

	rec := post(p, `{"model":"llama2-13b","messages":[]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"content":"hi"`) {
		t.Errorf("backend response not returned: %s", rec.Body.String())
	}
	if !strings.Contains(*gotBody, `"model":"llama2-13b"`) {
		t.Errorf("body not forwarded: %s", *gotBody)
	}
}

func TestProxy_RejectsUnservedModel(t *testing.T) {
	srv, _ := backend(t)
	p := NewProxy(srv.URL, []string{"llama2-13b"}, srv.Client())
	rec := post(p, `{"model":"gpt-4","messages":[]}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
}

func TestProxy_RequiresModel(t *testing.T) {
	p := NewProxy("http://unused", []string{"x"}, nil)
	rec := post(p, `{"messages":[]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

func TestProxy_BackendErrorIsBadGateway(t *testing.T) {
	// point at a closed server
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()
	p := NewProxy(url, []string{"llama2-13b"}, &http.Client{})
	rec := post(p, `{"model":"llama2-13b","messages":[]}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("want 502, got %d", rec.Code)
	}
}
