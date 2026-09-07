package manifest

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func digest(seed string) string {
	return "sha256:" + strings.Repeat(seed, 64)[:64]
}

func sample() Manifest {
	return Manifest{
		Version: 3, IssuedAt: "2026-09-07T00:00:00Z",
		Models: []Model{
			{ID: "gnodi/small", Engine: "ollama", Ref: "small:q4", Digest: digest("a"), MinVramGb: 8, ContextTokens: 8192},
			{ID: "gnodi/mid", Engine: "ollama", Ref: "mid:q4", Digest: digest("b"), MinVramGb: 16, ContextTokens: 8192},
			{ID: "gnodi/big", Engine: "ollama", Ref: "big:q4", Digest: digest("c"), MinVramGb: 48, ContextTokens: 8192},
		},
	}
}

func signed(t *testing.T, m Manifest, priv ed25519.PrivateKey) Signed {
	t.Helper()
	payload, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return Signed{
		KeyID:     "test",
		Payload:   base64.StdEncoding.EncodeToString(payload),
		Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(priv, payload)),
	}
}

func keypair(t *testing.T) (string, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(pub), priv
}

func TestVerify_RoundTrip(t *testing.T) {
	pub, priv := keypair(t)
	m, err := Verify(signed(t, sample(), priv), pub)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if m.Version != 3 || len(m.Models) != 3 {
		t.Errorf("got %+v", m)
	}
}

func TestVerify_RejectsTamperedPayload(t *testing.T) {
	pub, priv := keypair(t)
	s := signed(t, sample(), priv)

	var m Manifest
	raw, _ := base64.StdEncoding.DecodeString(s.Payload)
	_ = json.Unmarshal(raw, &m)
	m.Models[0].Ref = "malicious:latest" // swap the weights out
	tampered, _ := json.Marshal(m)
	s.Payload = base64.StdEncoding.EncodeToString(tampered)

	if _, err := Verify(s, pub); err == nil {
		t.Fatal("a tampered catalog verified")
	}
}

// A compromised gateway must not be able to tell the fleet what to download.
func TestVerify_RejectsAnotherKeysSignature(t *testing.T) {
	pinned, _ := keypair(t)
	_, attacker := keypair(t)
	if _, err := Verify(signed(t, sample(), attacker), pinned); err == nil {
		t.Fatal("a catalog signed by another key verified against the pinned one")
	}
}

func TestVerify_RejectsBadInputs(t *testing.T) {
	pub, priv := keypair(t)
	good := signed(t, sample(), priv)

	cases := map[string]Signed{
		"empty signature": {Payload: good.Payload, Signature: ""},
		"short signature": {Payload: good.Payload, Signature: base64.StdEncoding.EncodeToString([]byte("short"))},
		"bad base64":      {Payload: "!!!", Signature: good.Signature},
	}
	for name, s := range cases {
		if _, err := Verify(s, pub); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	if _, err := Verify(good, "not-a-key"); err == nil {
		t.Error("expected an error for a malformed public key")
	}
}

func TestVerify_RejectsPlaceholderDigest(t *testing.T) {
	pub, priv := keypair(t)
	m := sample()
	m.Models[0].Digest = "sha256:PENDING"
	// Pinning is the point; an unresolved digest must never be accepted.
	if _, err := Verify(signed(t, m, priv), pub); err == nil {
		t.Fatal("a catalog with an unfilled digest verified")
	}
}

func TestVerify_RejectsDuplicateIDs(t *testing.T) {
	pub, priv := keypair(t)
	m := sample()
	m.Models[1].ID = m.Models[0].ID
	if _, err := Verify(signed(t, m, priv), pub); err == nil {
		t.Fatal("a catalog with duplicate ids verified")
	}
}

func TestSelectForVram(t *testing.T) {
	m := sample()

	ids := func(models []Model) []string {
		out := make([]string, len(models))
		for i, x := range models {
			out[i] = x.ID
		}
		return out
	}

	// Largest-first, so the most capable model this card can run is pulled first.
	if got := ids(SelectForVram(m, 24, nil)); strings.Join(got, ",") != "gnodi/mid,gnodi/small" {
		t.Errorf("24GB selection = %v", got)
	}
	if got := ids(SelectForVram(m, 8, nil)); strings.Join(got, ",") != "gnodi/small" {
		t.Errorf("8GB selection = %v", got)
	}
	if got := SelectForVram(m, 4, nil); len(got) != 0 {
		t.Errorf("4GB should fit nothing, got %v", ids(got))
	}
	if got := ids(SelectForVram(m, 48, []string{"gnodi/big"})); strings.Join(got, ",") != "gnodi/big" {
		t.Errorf("allowlist ignored: %v", got)
	}
}

func TestOllamaBaseFrom(t *testing.T) {
	for in, want := range map[string]string{
		"http://localhost:11434/v1":  "http://localhost:11434",
		"http://localhost:11434/v1/": "http://localhost:11434",
		"http://host/v1":             "http://host",
	} {
		if got := OllamaBaseFrom(in); got != want {
			t.Errorf("OllamaBaseFrom(%q) = %q, want %q", in, got, want)
		}
	}
}

// --- Manager ---

type fakeOllama struct {
	local  map[string]string
	pulled []string
	// afterPull is merged into local once a pull succeeds.
	afterPull map[string]string
	failPull  bool
}

func (f *fakeOllama) server(t *testing.T) *Ollama {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			models := []map[string]string{}
			for name, d := range f.local {
				models = append(models, map[string]string{"name": name, "digest": d})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"models": models})
		case "/api/pull":
			var body struct {
				Model string `json:"model"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.pulled = append(f.pulled, body.Model)
			if f.failPull {
				fmt.Fprintln(w, `{"error":"no space left on device"}`)
				return
			}
			if d, ok := f.afterPull[body.Model]; ok {
				f.local[body.Model] = d
			}
			fmt.Fprintln(w, `{"status":"pulling manifest"}`)
			fmt.Fprintln(w, `{"status":"success"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return NewOllama(srv.URL, srv.Client())
}

func manifestServer(t *testing.T, s Signed) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(s)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestManager_PullsAndServesMatchingModels(t *testing.T) {
	pub, priv := keypair(t)
	fake := &fakeOllama{
		local:     map[string]string{},
		afterPull: map[string]string{"small:q4": digest("a"), "mid:q4": digest("b")},
	}
	m := &Manager{
		URL: manifestServer(t, signed(t, sample(), priv)), PublicKey: pub,
		VramGb: 16, AutoPull: true, Ollama: fake.server(t),
	}

	res, err := m.Sync(context.Background())
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if res.Version != 3 {
		t.Errorf("version = %d", res.Version)
	}
	if res.Catalog["gnodi/small"] != "small:q4" || res.Catalog["gnodi/mid"] != "mid:q4" {
		t.Errorf("catalog = %v", res.Catalog)
	}
	if _, present := res.Catalog["gnodi/big"]; present {
		t.Error("served a 48GB model on a 16GB card")
	}
	if len(fake.pulled) != 2 {
		t.Errorf("pulled = %v", fake.pulled)
	}
}

// The node holds weights the network did not publish under this id; serving
// them anyway is exactly the drift pinning exists to prevent.
func TestManager_RefusesDigestMismatch(t *testing.T) {
	pub, priv := keypair(t)
	fake := &fakeOllama{local: map[string]string{"small:q4": digest("z")}} // wrong weights
	m := &Manager{
		URL: manifestServer(t, signed(t, sample(), priv)), PublicKey: pub,
		VramGb: 8, AutoPull: false, Ollama: fake.server(t),
	}

	if _, err := m.Sync(context.Background()); err == nil {
		t.Fatal("expected sync to fail with nothing servable")
	}
	res, _ := m.Sync(context.Background())
	if _, present := res.Catalog["gnodi/small"]; present {
		t.Error("served a model whose digest does not match the manifest")
	}
	if res.Skipped["gnodi/small"] != "digest mismatch" {
		t.Errorf("skip reason = %q", res.Skipped["gnodi/small"])
	}
}

func TestManager_ReportsPullFailures(t *testing.T) {
	pub, priv := keypair(t)
	fake := &fakeOllama{local: map[string]string{"mid:q4": digest("b")}, failPull: true}
	m := &Manager{
		URL: manifestServer(t, signed(t, sample(), priv)), PublicKey: pub,
		VramGb: 16, AutoPull: true, Ollama: fake.server(t),
	}

	res, err := m.Sync(context.Background())
	if err != nil {
		t.Fatalf("a failed pull must not sink the whole sync: %v", err)
	}
	if res.Catalog["gnodi/mid"] != "mid:q4" {
		t.Error("the model that was already present should still serve")
	}
	if !strings.Contains(res.Skipped["gnodi/small"], "no space left") {
		t.Errorf("skip reason = %q", res.Skipped["gnodi/small"])
	}
}

func TestManager_FailsWhenNothingFits(t *testing.T) {
	pub, priv := keypair(t)
	fake := &fakeOllama{local: map[string]string{}}
	m := &Manager{
		URL: manifestServer(t, signed(t, sample(), priv)), PublicKey: pub,
		VramGb: 4, AutoPull: true, Ollama: fake.server(t),
	}
	if _, err := m.Sync(context.Background()); err == nil {
		t.Fatal("expected an error when no model fits the card")
	}
}
