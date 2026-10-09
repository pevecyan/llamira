package main

import (
	"bufio"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/term"

	"llamira/internal/api"
	"llamira/internal/config"
)

const (
	tokenCookie   = "llamira_token"
	sessionCookie = "llamira_session"
	sessionTTL    = 30 * 24 * time.Hour

	maxLoginAttempts = 5                // per client address, then one try per loginLockout
	loginLockout     = time.Minute      // wait after the last attempt once maxLoginAttempts are used
	loginForget      = 15 * time.Minute // quiet time after which an address starts over
)

// auth guards the TCP listener (the unix socket is guarded by its file permissions). It lets
// requests with the full token or a password session through, and requests with the read-only
// token (if configured) only where api.ReadOnlyAllowed says so. The token comes from the
// Authorization header, the token cookie, or a ?token= login link (which sets the cookie).
//
// With password_hash set, POST /api/login trades the password for a session cookie, and the UI
// files are served without login: the UI shows its login form when the API answers 401 with
// "login": true. A session is signed with sessionKey over its expiry and the password hash, so
// changing the password ends all sessions.
type auth struct {
	token, readToken string
	passwordHash     string // bcrypt
	sessionKey       []byte
	secure           bool // TLS: cookies are Secure
	attempts         loginAttempts
}

func (a *auth) handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Del(api.RoleHeader)
		if q := r.URL.Query().Get("token"); q != "" && a.match(q) != "" {
			// valid login link: remember it in a cookie and drop the token from the URL
			http.SetCookie(w, a.cookie(tokenCookie, q, 365*24*time.Hour))
			u := *r.URL
			qs := u.Query()
			qs.Del("token")
			u.RawQuery = qs.Encode()
			http.Redirect(w, r, u.RequestURI(), http.StatusFound)
			return
		}
		if a.passwordHash != "" && r.Method == http.MethodPost {
			switch r.URL.Path {
			case "/api/login":
				a.login(w, r)
				return
			case "/api/logout":
				a.logout(w, r)
				return
			}
		}
		role, session := a.role(r)
		switch role {
		case "full":
		case "read":
			if !api.ReadOnlyAllowed(r.Method, r.URL.Path) {
				http.Error(w, `{"error":"read-only access: this needs the full token"}`, http.StatusForbidden)
				return
			}
			r.Header.Set(api.RoleHeader, "read")
		default:
			if a.passwordHash == "" {
				http.Error(w, "unauthorized: open the UI with ?token=...", http.StatusUnauthorized)
				return
			}
			if p := path.Clean(r.URL.Path); (r.Method == http.MethodGet || r.Method == http.MethodHead) &&
				p != "/api" && !strings.HasPrefix(p, "/api/") && p != "/metrics" {
				next.ServeHTTP(w, r) // the UI files: no secrets, and they bring the login form
				return
			}
			writeAuthJSON(w, http.StatusUnauthorized, map[string]any{"error": "login required", "login": true})
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/api/session" {
			writeAuthJSON(w, http.StatusOK, map[string]bool{"logout": session})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// match tells which token got is: "full", "read" or "".
func (a *auth) match(got string) string {
	switch {
	case got == "":
	case a.token != "" && subtle.ConstantTimeCompare([]byte(got), []byte(a.token)) == 1:
		return "full"
	case a.readToken != "" && subtle.ConstantTimeCompare([]byte(got), []byte(a.readToken)) == 1:
		return "read"
	}
	return ""
}

// role authenticates a request by the Authorization header, else the session cookie, else the
// token cookie. session reports a password session.
func (a *auth) role(r *http.Request) (role string, session bool) {
	if got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "); got != "" {
		return a.match(got), false
	}
	if c, err := r.Cookie(sessionCookie); err == nil && a.validSession(c.Value) {
		return "full", true
	}
	if c, err := r.Cookie(tokenCookie); err == nil {
		return a.match(c.Value), false
	}
	return "", false
}

func (a *auth) login(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		writeAuthJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "login must be application/json"})
		return
	}
	ip := clientAddr(r)
	if wait := a.attempts.start(ip); wait > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		writeAuthJSON(w, http.StatusTooManyRequests, map[string]string{
			"error": fmt.Sprintf("too many wrong passwords: try again in %s", wait.Round(time.Second))})
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&req); err != nil {
		writeAuthJSON(w, http.StatusBadRequest, map[string]string{"error": `expected {"password": "..."}`})
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(a.passwordHash), []byte(req.Password)) != nil {
		writeAuthJSON(w, http.StatusUnauthorized, map[string]string{"error": "wrong password"})
		return
	}
	a.attempts.reset(ip)
	http.SetCookie(w, a.cookie(sessionCookie, a.sign(time.Now().Add(sessionTTL).Unix()), sessionTTL))
	w.WriteHeader(http.StatusNoContent)
}

// logout drops the session and token cookies of this browser. A copied session cookie stays
// valid until it expires or the password changes.
func (a *auth) logout(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		writeAuthJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "logout must be application/json"})
		return
	}
	for _, name := range []string{sessionCookie, tokenCookie} {
		c := a.cookie(name, "", 0)
		c.MaxAge = -1
		http.SetCookie(w, c)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *auth) cookie(name, value string, ttl time.Duration) *http.Cookie {
	return &http.Cookie{Name: name, Value: value, Path: "/", HttpOnly: true, Secure: a.secure,
		SameSite: http.SameSiteStrictMode, MaxAge: int(ttl.Seconds())}
}

// sign returns a session value: expiry (unix seconds) "." MAC.
func (a *auth) sign(exp int64) string {
	s := strconv.FormatInt(exp, 10)
	return s + "." + base64.RawURLEncoding.EncodeToString(a.mac(s))
}

func (a *auth) mac(exp string) []byte {
	m := hmac.New(sha256.New, a.sessionKey)
	m.Write([]byte(exp + "\n" + a.passwordHash))
	return m.Sum(nil)
}

func (a *auth) validSession(v string) bool {
	if a.passwordHash == "" || len(a.sessionKey) == 0 {
		return false
	}
	exp, sig, ok := strings.Cut(v, ".")
	if !ok {
		return false
	}
	t, err := strconv.ParseInt(exp, 10, 64)
	if err != nil || time.Now().Unix() > t {
		return false
	}
	got, err := base64.RawURLEncoding.DecodeString(sig)
	return err == nil && hmac.Equal(got, a.mac(exp))
}

// sessionKey reads the key that signs session cookies, creating it on first use. It lives in
// data_dir so sessions survive restarts.
func sessionKey(file string) ([]byte, error) {
	if b, err := os.ReadFile(file); err == nil && len(b) >= 32 {
		return b, nil
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	if err := os.WriteFile(file, b, 0o600); err != nil {
		return nil, fmt.Errorf("session key: %w", err)
	}
	return b, nil
}

// loginAttempts throttles password guessing per client address: after maxLoginAttempts each
// further try has to wait loginLockout after the previous one. An attempt counts when it
// starts, so parallel requests cannot slip past the limit; a successful login starts over.
type loginAttempts struct {
	mu sync.Mutex
	m  map[string]loginAttempt
}

type loginAttempt struct {
	n    int
	last time.Time
}

// start records an attempt from addr, or returns how long addr has to wait first.
func (l *loginAttempts) start(addr string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if l.m == nil {
		l.m = map[string]loginAttempt{}
	}
	for k, e := range l.m {
		if now.Sub(e.last) > loginForget {
			delete(l.m, k)
		}
	}
	e := l.m[addr]
	if e.n >= maxLoginAttempts {
		if wait := e.last.Add(loginLockout).Sub(now); wait > 0 {
			return wait
		}
	}
	l.m[addr] = loginAttempt{e.n + 1, now}
	return 0
}

func (l *loginAttempts) reset(addr string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.m, addr)
}

func clientAddr(r *http.Request) string {
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return h
	}
	return r.RemoteAddr
}

func writeAuthJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

// cmdPassword sets (or with --off removes) the UI login password in the server config.
func cmdPassword(args []string) error {
	fs := flag.NewFlagSet("password", flag.ExitOnError)
	cfgPath := fs.String("config", config.DefaultServerConfig, "config file")
	off := fs.Bool("off", false, "remove the password")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	cfg, err := config.LoadServer(*cfgPath)
	if err != nil {
		return err
	}
	if *off {
		if cfg.AuthToken == "" && !cfg.AllowUnauthenticated && cfg.Listen != "" && !config.IsLoopback(cfg.Listen) {
			return fmt.Errorf("%s has no auth_token: without the password the UI on %s would not start", *cfgPath, cfg.Listen)
		}
		cfg.PasswordHash = ""
	} else {
		pw, err := readNewPassword()
		if err != nil {
			return err
		}
		h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		cfg.PasswordHash = string(h)
		if cfg.AllowUnauthenticated {
			cfg.AllowUnauthenticated = false // it would override the password
			fmt.Println("✓ allow_unauthenticated turned off: the UI needs the password (or the token) now")
		}
	}
	b, err := config.MarshalServer(cfg)
	if err != nil {
		return err
	}
	if err := os.WriteFile(*cfgPath, b, 0o640); err != nil {
		if errors.Is(err, os.ErrPermission) {
			return fmt.Errorf("%w (run with sudo)", err)
		}
		return err
	}
	if *off {
		fmt.Printf("✓ password removed from %s\n", *cfgPath)
	} else {
		fmt.Printf("✓ password set in %s (existing logins end)\n", *cfgPath)
	}
	fmt.Println("Restart llamira to apply: sudo systemctl restart llamira")
	return nil
}

// readNewPassword asks twice on a terminal, or reads one line from stdin (scripts).
func readNewPassword() (string, error) {
	var pw string
	if fd := int(os.Stdin.Fd()); term.IsTerminal(fd) {
		ask := func(prompt string) (string, error) {
			fmt.Fprint(os.Stderr, prompt)
			b, err := term.ReadPassword(fd)
			fmt.Fprintln(os.Stderr)
			return string(b), err
		}
		var err error
		if pw, err = ask("New UI password: "); err != nil {
			return "", err
		}
		again, err := ask("Repeat: ")
		if err != nil {
			return "", err
		}
		if again != pw {
			return "", errors.New("the passwords differ")
		}
	} else {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		pw = strings.TrimRight(line, "\r\n")
	}
	if len(pw) < 6 {
		return "", errors.New("the password needs at least 6 characters")
	}
	return pw, nil
}
