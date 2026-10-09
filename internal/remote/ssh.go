// Package remote connects to llamira on other hosts over SSH: it tunnels HTTP to the
// remote unix socket (like `ssh -L`), and uploads/installs the binary for `deploy`.
package remote

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/kevinburke/ssh_config"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
	"golang.org/x/term"

	"llamira/internal/config"
)

// Conn is a lazily (re)connecting SSH connection to one host.
type Conn struct {
	Name string
	Host config.Host

	mu     sync.Mutex
	client *ssh.Client
}

func NewConn(name string, h config.Host) *Conn {
	if h.Socket == "" {
		h.Socket = config.DefaultSocket
	}
	return &Conn{Name: name, Host: h}
}

// target resolves user, address and identity files using ~/.ssh/config.
func (c *Conn) target() (usr, addr string, identities []string) {
	t := c.Host.SSH
	if i := strings.LastIndex(t, "@"); i >= 0 {
		usr, t = t[:i], t[i+1:]
	}
	hostPart, port := t, ""
	if h, p, err := net.SplitHostPort(t); err == nil {
		hostPart, port = h, p
	}
	alias := hostPart
	if hn := ssh_config.Get(alias, "HostName"); hn != "" {
		hostPart = hn
	}
	if port == "" {
		port = ssh_config.Get(alias, "Port")
	}
	if port == "" {
		port = "22"
	}
	if usr == "" {
		usr = ssh_config.Get(alias, "User")
	}
	if usr == "" {
		if u, err := user.Current(); err == nil {
			usr = u.Username
		}
	}
	if c.Host.Identity != "" {
		identities = append(identities, expand(c.Host.Identity))
	}
	for _, f := range ssh_config.GetAll(alias, "IdentityFile") {
		if f != "" && f != "~/.ssh/identity" {
			identities = append(identities, expand(f))
		}
	}
	home, _ := os.UserHomeDir()
	for _, f := range []string{"id_ed25519", "id_ecdsa", "id_rsa"} {
		identities = append(identities, filepath.Join(home, ".ssh", f))
	}
	return usr, net.JoinHostPort(hostPart, port), identities
}

func expand(p string) string {
	if strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, p[2:])
	}
	return p
}

// Client returns a connected SSH client, reconnecting if the previous one died.
func (c *Conn) Client() (*ssh.Client, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client != nil {
		if _, _, err := c.client.SendRequest("keepalive@openssh.com", true, nil); err == nil {
			return c.client, nil
		}
		c.client.Close()
		c.client = nil
	}
	cl, err := c.dial()
	if err != nil {
		return nil, err
	}
	c.client = cl
	go c.keepalive(cl)
	return cl, nil
}

func (c *Conn) keepalive(cl *ssh.Client) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for range t.C {
		if _, _, err := cl.SendRequest("keepalive@openssh.com", true, nil); err != nil {
			return
		}
	}
}

func (c *Conn) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client != nil {
		c.client.Close()
		c.client = nil
	}
}

func (c *Conn) dial() (*ssh.Client, error) {
	usr, addr, identities := c.target()
	hostKeyCB, algos, err := hostKeyCallback(addr)
	if err != nil {
		return nil, err
	}
	var auths []ssh.AuthMethod
	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		if ac, err := net.Dial("unix", sock); err == nil {
			auths = append(auths, ssh.PublicKeysCallback(agent.NewClient(ac).Signers))
		}
	}
	var signers []ssh.Signer
	seen := map[string]bool{}
	for _, f := range identities {
		if seen[f] {
			continue
		}
		seen[f] = true
		if s := loadKey(f); s != nil {
			signers = append(signers, s)
		}
	}
	if len(signers) > 0 {
		auths = append(auths, ssh.PublicKeys(signers...))
	}
	pw := passwordSource(usr, addr)
	auths = append(auths,
		ssh.PasswordCallback(pw),
		ssh.KeyboardInteractive(func(name, instr string, qs []string, echos []bool) ([]string, error) {
			ans := make([]string, len(qs))
			for i := range qs {
				p, err := pw()
				if err != nil {
					return nil, err
				}
				ans[i] = p
			}
			return ans, nil
		}),
	)
	cfg := &ssh.ClientConfig{
		User:              usr,
		Auth:              auths,
		HostKeyCallback:   hostKeyCB,
		HostKeyAlgorithms: algos,
		Timeout:           15 * time.Second,
	}
	cl, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		return nil, fmt.Errorf("ssh %s@%s: %w", usr, addr, err)
	}
	return cl, nil
}

// loadKey loads an unencrypted private key, or prompts for the passphrase on a terminal.
func loadKey(path string) ssh.Signer {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	s, err := ssh.ParsePrivateKey(b)
	if err == nil {
		return s
	}
	var missing *ssh.PassphraseMissingError
	if !errors.As(err, &missing) || !term.IsTerminal(int(os.Stdin.Fd())) {
		return nil
	}
	fmt.Fprintf(os.Stderr, "Passphrase for %s: ", path)
	pass, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return nil
	}
	s, err = ssh.ParsePrivateKeyWithPassphrase(b, pass)
	if err != nil {
		return nil
	}
	return s
}

// passwordSource returns the password from $SSHPASS (same convention as sshpass), or prompts
// on the terminal once.
func passwordSource(usr, addr string) func() (string, error) {
	var once sync.Once
	var pw string
	var perr error
	return func() (string, error) {
		once.Do(func() {
			if p := os.Getenv("SSHPASS"); p != "" {
				pw = p
				return
			}
			if !term.IsTerminal(int(os.Stdin.Fd())) {
				perr = errors.New("password required but no terminal (set SSHPASS or use a key)")
				return
			}
			fmt.Fprintf(os.Stderr, "%s@%s's password: ", usr, addr)
			b, err := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Fprintln(os.Stderr)
			pw, perr = string(b), err
		})
		return pw, perr
	}
}

// hostKeyCallback verifies host keys against ~/.ssh/known_hosts. Unknown hosts are accepted
// only after confirmation on a terminal, and then appended (trust on first use).
// It also returns the host key algorithms already known for addr, so the server offers a key
// type that known_hosts can verify.
func hostKeyCallback(addr string) (ssh.HostKeyCallback, []string, error) {
	home, _ := os.UserHomeDir()
	path := filepath.Join(home, ".ssh", "known_hosts")
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		os.MkdirAll(filepath.Dir(path), 0o700)
		os.WriteFile(path, nil, 0o600)
	}
	kh, err := knownhosts.New(path)
	if err != nil {
		return nil, nil, err
	}
	algos := knownAlgorithms(path, knownhosts.Normalize(addr))
	cb := func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		err := kh(hostname, remote, key)
		var ke *knownhosts.KeyError
		if !errors.As(err, &ke) || len(ke.Want) > 0 {
			return err // nil (known), or a mismatch: never accept
		}
		fp := ssh.FingerprintSHA256(key)
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			return fmt.Errorf("unknown host %s (%s %s); connect once interactively to accept it", hostname, key.Type(), fp)
		}
		fmt.Fprintf(os.Stderr, "The authenticity of host %s can't be established.\n%s key fingerprint is %s.\nAccept and remember it? [y/N] ", hostname, key.Type(), fp)
		ans, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if strings.ToLower(strings.TrimSpace(ans)) != "y" {
			return errors.New("host key rejected")
		}
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = fmt.Fprintln(f, knownhosts.Line([]string{knownhosts.Normalize(hostname)}, key))
		return err
	}
	return cb, algos, nil
}

func knownAlgorithms(path, host string) []string {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var algos []string
	for len(b) > 0 {
		_, hosts, key, _, rest, err := ssh.ParseKnownHosts(b)
		if err != nil {
			break
		}
		b = rest
		for _, h := range hosts {
			if h == host {
				t := key.Type()
				if t == ssh.KeyAlgoRSA {
					algos = append(algos, ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256)
				}
				algos = append(algos, t)
			}
		}
	}
	return algos
}

// DialSocket opens a stream to the remote API unix socket.
func (c *Conn) DialSocket(ctx context.Context) (net.Conn, error) {
	cl, err := c.Client()
	if err != nil {
		return nil, err
	}
	conn, err := cl.DialContext(ctx, "unix", c.Host.Socket)
	if err != nil {
		return nil, fmt.Errorf("%s: cannot reach %s (is llamira running there?): %w", c.Name, c.Host.Socket, err)
	}
	return conn, nil
}

// Transport returns an HTTP transport whose connections go to the remote socket.
func (c *Conn) Transport() *http.Transport {
	return &http.Transport{
		DialContext:         func(ctx context.Context, _, _ string) (net.Conn, error) { return c.DialSocket(ctx) },
		MaxIdleConnsPerHost: 8,
		IdleConnTimeout:     60 * time.Second,
	}
}

// Proxy returns a reverse proxy forwarding /api/hosts/<name>/X to the remote /api/X.
func (c *Conn) Proxy(prefix string) http.Handler {
	return &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.Out.URL.Scheme = "http"
			r.Out.URL.Host = "llamira"
			r.Out.URL.Path = "/api" + strings.TrimPrefix(r.In.URL.Path, prefix)
			r.Out.URL.RawPath = ""
			r.Out.Host = "llamira"
		},
		Transport:     c.Transport(),
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprintf(w, "{\"error\":%q}\n", err.Error())
		},
	}
}

// Run executes a command on the remote host and returns combined output.
func (c *Conn) Run(cmd string, stdin io.Reader) (string, error) {
	cl, err := c.Client()
	if err != nil {
		return "", err
	}
	sess, err := cl.NewSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()
	// stdout and stderr are copied by separate goroutines: they must not share an
	// unsynchronised bytes.Buffer (its ReadFrom races and loses output).
	out := &lockedBuffer{}
	sess.Stdout, sess.Stderr = out, out
	sess.Stdin = stdin
	err = sess.Run(cmd)
	return out.String(), err
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

// Write deliberately hides bytes.Buffer.ReadFrom from io.Copy.
func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// RunStream executes a command, streaming its output to w.
func (c *Conn) RunStream(cmd string, stdin io.Reader, w io.Writer) error {
	cl, err := c.Client()
	if err != nil {
		return err
	}
	sess, err := cl.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()
	sess.Stdout, sess.Stderr, sess.Stdin = w, w, stdin
	return sess.Run(cmd)
}
