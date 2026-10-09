package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"llamira/internal/api"
)

func passwordAuth(t *testing.T, password string) *auth {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	return &auth{token: "full-secret", passwordHash: string(h), sessionKey: []byte(strings.Repeat("k", 32)), secure: true}
}

func TestAuthPassword(t *testing.T) {
	a := passwordAuth(t, "correct horse")
	var role string
	h := a.handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		role = r.Header.Get(api.RoleHeader)
		w.WriteHeader(204)
	}))
	do := func(method, path, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		for _, c := range cookies {
			r.AddCookie(c)
		}
		w := httptest.NewRecorder()
		role = "unset"
		h.ServeHTTP(w, r)
		return w
	}

	// without login: the UI files load, the API and metrics do not
	for _, p := range []string{"/", "/presets", "/assets/index-abc.js", "/llamira-icon-64.png"} {
		if w := do("GET", p, ""); w.Code != 204 {
			t.Errorf("GET %s without login: %d, want the UI", p, w.Code)
		}
	}
	for _, p := range []string{"/api/status", "/api/hosts", "/api/hosts/local/router/key", "/metrics", "/x/../api/status", "/api"} {
		if w := do("GET", p, ""); w.Code != 401 || !strings.Contains(w.Body.String(), `"login":true`) {
			t.Errorf("GET %s without login: %d %s", p, w.Code, w.Body)
		}
	}
	if w := do("POST", "/presets", "{}"); w.Code != 401 {
		t.Errorf("POST to a UI path without login: %d", w.Code)
	}

	// forged sessions
	for _, v := range []string{"authenticated", "99999999999.AAAA", a.sign(time.Now().Add(-time.Minute).Unix())} {
		if w := do("GET", "/api/status", "", &http.Cookie{Name: sessionCookie, Value: v}); w.Code != 401 {
			t.Errorf("session %q: %d", v, w.Code)
		}
	}

	// wrong password, then the right one
	if w := do("POST", "/api/login", `{"password":"wrong"}`); w.Code != 401 || len(w.Result().Cookies()) != 0 {
		t.Errorf("wrong password: %d %v", w.Code, w.Result().Cookies())
	}
	w := do("POST", "/api/login", `{"password":"correct horse"}`)
	cs := w.Result().Cookies()
	if w.Code != 204 || len(cs) != 1 || cs[0].Name != sessionCookie || !cs[0].HttpOnly || !cs[0].Secure {
		t.Fatalf("login: %d %+v", w.Code, cs)
	}
	session := cs[0]
	if w := do("POST", "/api/models/load", "{}", session); w.Code != 204 || role != "" {
		t.Errorf("with session: %d role %q", w.Code, role)
	}
	if w := do("GET", "/api/session", "", session); w.Code != 200 || !strings.Contains(w.Body.String(), `"logout":true`) {
		t.Errorf("session info: %d %s", w.Code, w.Body)
	}

	// the token keeps working next to the password
	r := httptest.NewRequest("GET", "/api/status", nil)
	r.Header.Set("Authorization", "Bearer full-secret")
	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, r)
	if rw.Code != 204 {
		t.Errorf("token in password mode: %d", rw.Code)
	}

	// a new password ends the session
	b := passwordAuth(t, "another one")
	if b.validSession(session.Value) {
		t.Error("session must not survive a password change")
	}

	// logout drops both cookies
	w = do("POST", "/api/logout", "{}", session)
	if cs := w.Result().Cookies(); w.Code != 204 || len(cs) != 2 || cs[0].MaxAge >= 0 || cs[1].MaxAge >= 0 {
		t.Errorf("logout: %d %+v", w.Code, cs)
	}
}

func TestAuthLoginNeedsJSON(t *testing.T) {
	h := passwordAuth(t, "correct horse").handler(http.NotFoundHandler())
	r := httptest.NewRequest("POST", "/api/login", strings.NewReader("password=correct+horse"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnsupportedMediaType {
		t.Errorf("form login: %d", w.Code)
	}
}

func TestAuthLoginThrottle(t *testing.T) {
	h := passwordAuth(t, "correct horse").handler(http.NotFoundHandler())
	login := func(addr, pw string) int {
		r := httptest.NewRequest("POST", "/api/login", strings.NewReader(`{"password":"`+pw+`"}`))
		r.Header.Set("Content-Type", "application/json")
		r.RemoteAddr = addr + ":40000"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	for i := range maxLoginAttempts {
		if c := login("10.0.0.9", "guess"); c != 401 {
			t.Fatalf("attempt %d: %d", i+1, c)
		}
	}
	if c := login("10.0.0.9", "correct horse"); c != http.StatusTooManyRequests {
		t.Errorf("after %d wrong passwords: %d, want 429", maxLoginAttempts, c)
	}
	if c := login("10.0.0.10", "correct horse"); c != 204 {
		t.Errorf("other address: %d", c)
	}
}

func TestAuthTokenOnlyHasNoLogin(t *testing.T) {
	h := (&auth{token: "full-secret"}).handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	for _, c := range []struct{ method, path string }{{"GET", "/"}, {"POST", "/api/login"}} {
		r := httptest.NewRequest(c.method, c.path, strings.NewReader(`{"password":""}`))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 401 {
			t.Errorf("%s %s without password_hash: %d", c.method, c.path, w.Code)
		}
	}
}

func TestSessionKey(t *testing.T) {
	file := t.TempDir() + "/session.key"
	k1, err := sessionKey(file)
	if err != nil || len(k1) != 32 {
		t.Fatalf("%x %v", k1, err)
	}
	if k2, _ := sessionKey(file); string(k2) != string(k1) {
		t.Error("the key must be kept across restarts")
	}
}
