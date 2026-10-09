// Package config holds the server (host-side) and client (laptop-side) configuration.
package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

const (
	DefaultServerConfig = "/etc/llamira/config.yaml"
	DefaultSocket       = "/run/llamira/api.sock"
	DefaultListen       = "127.0.0.1:7070"
)

// Server is the configuration of `llamira serve` on the llama host.
type Server struct {
	Listen    string `yaml:"listen" json:"listen"`         // TCP address for the UI/API ("" disables)
	Socket    string `yaml:"socket" json:"socket"`         // unix socket for SSH tunnels and the CLI ("" disables)
	AuthToken string `yaml:"auth_token" json:"auth_token"` // required for non-loopback Listen addresses...
	// ...unless explicitly turned off: anyone who can reach Listen gets full access.
	AllowUnauthenticated bool   `yaml:"allow_unauthenticated" json:"allow_unauthenticated"`
	ReadToken            string `yaml:"read_token,omitempty" json:"read_token,omitempty"`       // optional: allows GET requests only (dashboards)
	PasswordHash         string `yaml:"password_hash,omitempty" json:"password_hash,omitempty"` // optional: bcrypt hash of a UI login password (`llamira password`)
	TLS                  string `yaml:"tls,omitempty" json:"tls,omitempty"`                     // "self-signed": generate a certificate in data_dir
	TLSCert              string `yaml:"tls_cert,omitempty" json:"tls_cert,omitempty"`           // PEM certificate for Listen (with tls_key)
	TLSKey               string `yaml:"tls_key,omitempty" json:"tls_key,omitempty"`
	DataDir              string `yaml:"data_dir" json:"data_dir"`       // SQLite database and backups
	PresetFile           string `yaml:"preset_file" json:"preset_file"` // router --models-preset file
	Backups              int    `yaml:"backups" json:"backups"`         // number of preset backups to keep
	HFToken              string `yaml:"hf_token" json:"hf_token"`       // optional Hugging Face token (gated/private repos, rate limits)
	CatalogURL           string `yaml:"catalog_url" json:"catalog_url"` // optional: recommendation catalogue (JSON) replacing the built-in one
	Router               Router `yaml:"router" json:"router"`
}

// Router describes the llama-server router instance being managed.
type Router struct {
	URL        string   `yaml:"url" json:"url"`
	APIKey     string   `yaml:"api_key" json:"api_key"`
	APIKeyFile string   `yaml:"api_key_file" json:"api_key_file"` // router API key managed by the manager (see Router settings)
	Binary     string   `yaml:"binary" json:"binary"`
	WorkDir    string   `yaml:"workdir" json:"workdir"`
	ModelsDir  string   `yaml:"models_dir" json:"models_dir"`
	CacheDir   string   `yaml:"cache_dir" json:"cache_dir"`   // router download cache; empty = LLAMA_CACHE of the systemd unit
	BuildsDir  string   `yaml:"builds_dir" json:"builds_dir"` // llama.cpp versions built by the updater (default /opt/llama.cpp-builds)
	Service    string   `yaml:"service" json:"service"`       // systemd unit name
	Env        []string `yaml:"env" json:"env"`               // extra env for dry-run instances; empty = read from the systemd unit
}

func DefaultServer() Server {
	return Server{
		Listen:     DefaultListen,
		Socket:     DefaultSocket,
		DataDir:    "/var/lib/llamira",
		PresetFile: "/var/lib/llamira/models.ini",
		Backups:    20,
		Router: Router{
			URL:        "http://127.0.0.1:8080",
			Binary:     "/opt/llama.cpp/build/bin/llama-server",
			WorkDir:    "/opt/llama.cpp",
			ModelsDir:  "/models",
			Service:    "llama-server.service",
			APIKeyFile: "/etc/llamira/router-api-key",
		},
	}
}

// LoadServer reads the server config. A missing file yields the defaults.
func LoadServer(path string) (Server, error) {
	c := DefaultServer()
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := yaml.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// TLSEnabled reports whether Listen serves HTTPS.
func (c Server) TLSEnabled() bool {
	return c.TLS == "self-signed" || (c.TLSCert != "" && c.TLSKey != "")
}

// IsLoopback reports whether a listen address only accepts local connections.
func IsLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// MarshalServer renders a server config as YAML.
func MarshalServer(c Server) ([]byte, error) { return yaml.Marshal(c) }

// Host is one managed llama host, as seen from the laptop.
type Host struct {
	SSH      string `yaml:"ssh"`                // user@host[:port] or an ~/.ssh/config alias
	Socket   string `yaml:"socket,omitempty"`   // remote API socket (default /run/llamira/api.sock)
	Identity string `yaml:"identity,omitempty"` // private key file (optional; agent and ssh config are used too)
}

// Client is ~/.config/llamira/hosts.yaml.
type Client struct {
	Default string          `yaml:"default,omitempty"`
	Hosts   map[string]Host `yaml:"hosts"`
}

func ClientPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return filepath.Join(dir, "llamira", "hosts.yaml")
}

func LoadClient(path string) (Client, error) {
	var c Client
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Client{Hosts: map[string]Host{}}, nil
	}
	if err != nil {
		return c, err
	}
	if err := yaml.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	if c.Hosts == nil {
		c.Hosts = map[string]Host{}
	}
	return c, nil
}

func SaveClient(path string, c Client) error {
	b, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

// Names returns host names, sorted.
func (c Client) Names() []string {
	var n []string
	for k := range c.Hosts {
		n = append(n, k)
	}
	sort.Strings(n)
	return n
}

// Resolve turns a host name or a literal user@host target into a named Host.
// Literal targets get a name derived from the host part.
func (c Client) Resolve(target string) (string, Host, error) {
	if target == "" {
		target = c.Default
		if target == "" && len(c.Hosts) == 1 {
			target = c.Names()[0]
		}
		if target == "" {
			return "", Host{}, errors.New("no host given and no default host in " + ClientPath())
		}
	}
	if h, ok := c.Hosts[target]; ok {
		return target, h, nil
	}
	return NameFromTarget(target), Host{SSH: target}, nil
}

// NameFromTarget derives a short host name from "user@host:port".
func NameFromTarget(t string) string {
	for i := len(t) - 1; i >= 0; i-- {
		if t[i] == '@' {
			t = t[i+1:]
			break
		}
	}
	for i := 0; i < len(t); i++ {
		if t[i] == ':' {
			return t[:i]
		}
	}
	return t
}
