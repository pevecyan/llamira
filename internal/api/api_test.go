package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequireJSON(t *testing.T) {
	h := requireJSON(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	cases := []struct {
		method, ct string
		want       int
	}{
		{"GET", "", 204},
		{"POST", "application/json", 204},
		{"POST", "application/json; charset=utf-8", 204},
		{"POST", "text/plain", 415},
		{"POST", "application/x-www-form-urlencoded", 415},
		{"POST", "", 415},
		{"PUT", "multipart/form-data", 415},
	}
	for _, c := range cases {
		r := httptest.NewRequest(c.method, "/api/x", strings.NewReader("{}"))
		if c.ct != "" {
			r.Header.Set("Content-Type", c.ct)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != c.want {
			t.Errorf("%s %q: got %d, want %d", c.method, c.ct, w.Code, c.want)
		}
	}
}

func TestPresetName(t *testing.T) {
	taken := map[string]bool{"qwen3.5-9b-q4_k_m": true}
	for in, want := range map[string]string{
		"unsloth/Qwen3.5-9B-GGUF:Q4_K_M":         "qwen3.5-9b-q4_k_m-2",
		"ggml-org/embeddinggemma-300M-GGUF:Q8_0": "embeddinggemma-300m-q8_0",
		"LiquidAI/LFM2.5-2.6B-GGUF:Q4_K_M":       "lfm2.5-2.6b-q4_k_m",
	} {
		if got := presetName(in, func(n string) bool { return taken[n] }); got != want {
			t.Errorf("presetName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestReadOnlyAllowed(t *testing.T) {
	for _, c := range []struct {
		method, path string
		want         bool
	}{
		{"GET", "/api/status", true},
		{"GET", "/api/hosts/local/models", true},
		{"GET", "/", true},
		{"GET", "/metrics", true},
		{"POST", "/api/models/load", false},
		{"POST", "/api/hosts/local/chat", false},
		{"DELETE", "/api/bench/1", false},
		{"GET", "/api/router/key", false},
		{"GET", "/api/hosts/local/router/key", false},
		{"GET", "/api/hosts/llama/export", false},
		{"GET", "/api/alerts", false},
	} {
		if got := ReadOnlyAllowed(c.method, c.path); got != c.want {
			t.Errorf("%s %s = %v, want %v", c.method, c.path, got, c.want)
		}
	}
}
