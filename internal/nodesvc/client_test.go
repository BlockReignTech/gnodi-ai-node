package nodesvc

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

type captured struct {
	path    string
	headers http.Header
	body    map[string]any
}

func newServer(t *testing.T, status int) (*httptest.Server, *captured) {
	t.Helper()
	cap := &captured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.path = r.URL.Path
		cap.headers = r.Header.Clone()
		if b, _ := io.ReadAll(r.Body); len(b) > 0 {
			_ = json.Unmarshal(b, &cap.body)
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, cap
}

func TestActivate(t *testing.T) {
	srv, cap := newServer(t, http.StatusOK)
	c := New(srv.URL, "KEY-123", srv.Client())
	if err := c.Activate(context.Background(), "v1.2.3"); err != nil {
		t.Fatal(err)
	}
	if cap.path != "/nodes/activate" {
		t.Errorf("path = %q", cap.path)
	}
	if cap.body["licenseKey"] != "KEY-123" || cap.body["nodeVersion"] != "v1.2.3" {
		t.Errorf("body = %v", cap.body)
	}
}

func TestReportCapabilities(t *testing.T) {
	srv, cap := newServer(t, http.StatusOK)
	c := New(srv.URL, "KEY-123", srv.Client())
	if err := c.ReportCapabilities(context.Background(), "https://node1", []string{"llama2-13b"}); err != nil {
		t.Fatal(err)
	}
	if cap.path != "/nodes/capabilities" {
		t.Errorf("path = %q", cap.path)
	}
	if cap.headers.Get("X-LICENSE-KEY") != "KEY-123" {
		t.Errorf("missing license header: %v", cap.headers)
	}
	if cap.body["endpoint"] != "https://node1" {
		t.Errorf("endpoint = %v", cap.body["endpoint"])
	}
	if models, ok := cap.body["models"].([]any); !ok || len(models) != 1 || models[0] != "llama2-13b" {
		t.Errorf("models = %v", cap.body["models"])
	}
}

func TestHeartbeat_WithMetrics(t *testing.T) {
	srv, cap := newServer(t, http.StatusOK)
	c := New(srv.URL, "KEY-123", srv.Client())
	h := int64(469545)
	cu := false
	peers := 7
	err := c.Heartbeat(context.Background(), "v1", Metrics{BlockHeight: &h, CatchingUp: &cu, NumPeers: &peers})
	if err != nil {
		t.Fatal(err)
	}
	if cap.headers.Get("X-LICENSE-KEY") != "KEY-123" ||
		cap.headers.Get("X-BLOCK-HEIGHT") != "469545" ||
		cap.headers.Get("X-CATCHING-UP") != "false" ||
		cap.headers.Get("X-NUM-PEERS") != "7" {
		t.Errorf("heartbeat headers wrong: %v", cap.headers)
	}
}

func TestNon2xxIsError(t *testing.T) {
	srv, _ := newServer(t, http.StatusUnauthorized)
	c := New(srv.URL, "BAD", srv.Client())
	if err := c.Activate(context.Background(), "v1"); err == nil {
		t.Fatal("expected error on 401")
	}
}
