package llamacpp

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestVersions(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/ggml-org/llama.cpp/tags":
			fmt.Fprint(w, `[{"name":"v0.9.0"},{"name":"v0.10.0"},{"name":"b11521"},{"name":"v0.8.1"},{"name":"tmp-testing-0"},{"name":"v0.1.0"}]`)
		case strings.HasPrefix(r.URL.Path, "/repos/ggml-org/llama.cpp/releases/tags/"):
			v := strings.TrimPrefix(r.URL.Path, "/repos/ggml-org/llama.cpp/releases/tags/")
			if v == "v0.1.0" {
				http.NotFound(w, r) // a tag without a release
				return
			}
			fmt.Fprintf(w, `{"published_at":"2026-10-05T16:56:22Z","html_url":"https://x/%s","body":"## Overview\n\nllama.cpp %s introduces things","assets":[{"name":"nightly-tag.txt","browser_download_url":"%s/dl/%s"}]}`, v, v, srv.URL, v)
		case strings.HasPrefix(r.URL.Path, "/dl/"):
			fmt.Fprint(w, map[string]string{"/dl/v0.10.0": "b11429\n", "/dl/v0.9.0": "b11300\n", "/dl/v0.8.1": "b11000\n"}[r.URL.Path])
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	GitHubAPI = srv.URL
	defer func() { GitHubAPI = "https://api.github.com"; verCache = nil }()

	vs, err := Versions(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, v := range vs {
		got = append(got, v.Version+"="+v.Tag)
	}
	if strings.Join(got, " ") != "v0.10.0=b11429 v0.9.0=b11300 v0.8.1=b11000" {
		t.Errorf("versions %v", got)
	}
	if vs[0].Notes != "llama.cpp v0.10.0 introduces things" {
		t.Errorf("notes %q (headings must be skipped)", vs[0].Notes)
	}
	if v, n := VersionOf(vs, 11036); v != "v0.8.1" || n != 36 {
		t.Errorf("VersionOf(11036) = %s +%d", v, n)
	}
	if v, n := VersionOf(vs, 11429); v != "v0.10.0" || n != 0 {
		t.Errorf("VersionOf(11429) = %s +%d", v, n)
	}
	if v, _ := VersionOf(vs, 900); v != "" {
		t.Errorf("VersionOf(900) = %s", v)
	}
}
