package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"llamira/internal/api"
)

func TestRequireToken(t *testing.T) {
	var role string
	h := (&auth{token: "full-secret", readToken: "read-secret", secure: true}).handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		role = r.Header.Get(api.RoleHeader)
		w.WriteHeader(204)
	}))
	do := func(method, path, bearer, cookie string, hdr map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader("{}"))
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: "llamira_token", Value: cookie})
		}
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		role = "unset"
		h.ServeHTTP(w, r)
		return w
	}

	if w := do("GET", "/api/status", "", "", nil); w.Code != 401 {
		t.Errorf("no token: %d", w.Code)
	}
	if w := do("GET", "/api/status", "wrong", "", nil); w.Code != 401 {
		t.Errorf("wrong token: %d", w.Code)
	}
	if w := do("POST", "/api/models/load", "full-secret", "", map[string]string{api.RoleHeader: "read"}); w.Code != 204 || role != "" {
		t.Errorf("full token: %d role %q (a client-sent role header must be dropped)", w.Code, role)
	}
	if w := do("GET", "/api/status", "", "read-secret", nil); w.Code != 204 || role != "read" {
		t.Errorf("read token GET: %d role %q", w.Code, role)
	}
	if w := do("POST", "/api/models/load", "read-secret", "", nil); w.Code != 403 {
		t.Errorf("read token POST: %d", w.Code)
	}
	if w := do("GET", "/api/hosts/local/router/key", "read-secret", "", nil); w.Code != 403 {
		t.Errorf("read token secret GET: %d", w.Code)
	}

	// login link: sets a Secure cookie and redirects without the token
	w := do("GET", "/presets?token=read-secret&section=x", "", "", nil)
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/presets?section=x" {
		t.Errorf("login link: %d %q", w.Code, w.Header().Get("Location"))
	}
	if c := w.Result().Cookies(); len(c) != 1 || c[0].Value != "read-secret" || !c[0].Secure || !c[0].HttpOnly {
		t.Errorf("cookie %+v", c)
	}
}

func TestRequireTokenWithoutReadToken(t *testing.T) {
	h := (&auth{token: "full-secret"}).handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	r := httptest.NewRequest("GET", "/api/status", nil)
	r.Header.Set("Authorization", "Bearer ")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Errorf("empty bearer must not match an unset read token: %d", w.Code)
	}
}
