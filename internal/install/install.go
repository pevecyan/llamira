// Package install sets up llamira on a llama host (run as root): service user,
// sudoers rule, systemd unit, and optionally "adopts" the llama-server router so that
// per-model settings live in the preset file instead of on its command line.
package install

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"llamira/internal/apply"
	"llamira/internal/config"
	"llamira/internal/llamacpp"
	"llamira/internal/presets"
	"llamira/internal/system"
)

const (
	User       = "llamira"
	BinPath    = "/usr/local/bin/llamira"
	UnitPath   = "/etc/systemd/system/llamira.service"
	Sudoers    = "/etc/sudoers.d/llamira"
	DropInName = "llamira.conf"
)

type Options struct {
	ConfigPath string
	Adopt      bool
	Yes        bool   // don't ask before restarting llama-server
	Listen     string // if set, the TCP address for the UI (a non-loopback address gets an auth token)
	Auth       string // "token", "none" (no authentication), or "" to keep the current setting
	TLS        string // "self-signed", "off", or "" to keep the current setting
	ReadToken  bool   // also create a read-only token (GET only) if there is none
	LlamaFrom  string // fresh install: "prebuilt" (default) or "source"
	LlamaRef   string // fresh install: release tag (default: latest)
	Out        io.Writer
	Confirm    func(question string) bool
}

func (o *Options) logf(format string, a ...any) { fmt.Fprintf(o.Out, format+"\n", a...) }

func run(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Run performs the installation. It is idempotent.
func Run(ctx context.Context, o Options) error {
	if os.Geteuid() != 0 {
		return errors.New("install must run as root")
	}
	cfg, err := config.LoadServer(o.ConfigPath)
	if err != nil {
		return err
	}

	// Binary
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if self, _ = filepath.EvalSymlinks(self); self != BinPath {
		if err := copyFile(self, BinPath, 0o755); err != nil {
			return fmt.Errorf("install binary: %w", err)
		}
		o.logf("✓ binary installed at %s", BinPath)
	}

	// Config
	_, statErr := os.Stat(o.ConfigPath)
	if o.Listen != "" && o.Listen != cfg.Listen {
		cfg.Listen = o.Listen
		statErr = os.ErrNotExist // force a rewrite
	}
	switch o.Auth {
	case "none":
		if !cfg.AllowUnauthenticated {
			cfg.AllowUnauthenticated = true
			statErr = os.ErrNotExist
		}
	case "token":
		if cfg.AllowUnauthenticated {
			cfg.AllowUnauthenticated = false
			statErr = os.ErrNotExist
		}
	case "":
	default:
		return fmt.Errorf("--auth must be token or none, not %q", o.Auth)
	}
	switch o.TLS {
	case "self-signed":
		if cfg.TLS != "self-signed" {
			cfg.TLS = "self-signed"
			statErr = os.ErrNotExist
		}
	case "off":
		if cfg.TLSEnabled() {
			cfg.TLS, cfg.TLSCert, cfg.TLSKey = "", "", ""
			statErr = os.ErrNotExist
		}
	case "":
	default:
		return fmt.Errorf("--tls must be self-signed or off, not %q", o.TLS)
	}
	if o.ReadToken && cfg.ReadToken == "" {
		cfg.ReadToken = randomToken()
		statErr = os.ErrNotExist
	}
	if !config.IsLoopback(cfg.Listen) && cfg.Listen != "" && cfg.AuthToken == "" && !cfg.AllowUnauthenticated {
		cfg.AuthToken = randomToken()
		statErr = os.ErrNotExist
	}
	if errors.Is(statErr, os.ErrNotExist) {
		b, _ := config.MarshalServer(cfg)
		os.MkdirAll(filepath.Dir(o.ConfigPath), 0o755)
		if err := os.WriteFile(o.ConfigPath, b, 0o640); err != nil {
			return err
		}
		o.logf("✓ wrote config %s (listen %s)", o.ConfigPath, cfg.Listen)
	}

	// User
	if exec.Command("id", "-u", User).Run() != nil {
		if err := run("useradd", "--system", "--user-group", "--no-create-home",
			"--home-dir", cfg.DataDir, "--shell", "/usr/sbin/nologin", User); err != nil {
			return err
		}
		o.logf("✓ created system user %s", User)
	}
	if err := run("usermod", "-aG", "systemd-journal", User); err != nil {
		return err
	}
	_ = run("chgrp", User, o.ConfigPath)

	// Data dir (preset file, backups, database)
	if err := os.MkdirAll(filepath.Join(cfg.DataDir, "backups"), 0o755); err != nil {
		return err
	}
	if err := run("chown", "-R", User+":"+User, cfg.DataDir); err != nil {
		return err
	}
	o.logf("✓ data dir %s owned by %s", cfg.DataDir, User)

	// sudoers: only restarting the router service
	// restarting the router, plus the privileged helper (which validates its own arguments)
	rule := fmt.Sprintf("# Managed by llamira\n%[1]s ALL=(root) NOPASSWD: /usr/bin/systemctl restart %[2]s\n"+
		"%[1]s ALL=(root) NOPASSWD: %[3]s privileged *\n", User, cfg.Router.Service, BinPath)
	tmp := Sudoers + ".tmp"
	if err := os.WriteFile(tmp, []byte(rule), 0o440); err != nil {
		return err
	}
	if err := run("visudo", "-cf", tmp); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, Sudoers); err != nil {
		return err
	}
	o.logf("✓ sudoers rule %s (restart %s, llamira privileged helper)", Sudoers, cfg.Router.Service)

	// no llama-server yet (or one from an earlier, possibly interrupted, fresh install):
	// install llama.cpp and the router (idempotent); otherwise adopt the existing router
	if !routerUnitExists(cfg.Router.Service) || loadManifest(cfg).Fresh {
		if !routerUnitExists(cfg.Router.Service) {
			o.logf("No %s found on this host.", cfg.Router.Service)
		}
		if err := FreshInstall(ctx, &cfg, o); err != nil {
			return err
		}
		if b, err := config.MarshalServer(cfg); err == nil { // remember the installed binary
			_ = os.WriteFile(o.ConfigPath, b, 0o640)
			_ = run("chgrp", User, o.ConfigPath)
		}
		_ = run("chown", "-R", User+":"+User, cfg.DataDir)
	}
	if o.Adopt {
		if err := Adopt(ctx, cfg, o); err != nil {
			return err
		}
		_ = run("chown", "-R", User+":"+User, cfg.DataDir)
	}

	// Unit
	unit := fmt.Sprintf(`[Unit]
Description=llamira (management companion for the llama.cpp router)
After=network-online.target %[1]s

[Service]
User=%[2]s
Group=%[2]s
SupplementaryGroups=systemd-journal
ExecStart=%[3]s serve --config %[4]s
RuntimeDirectory=llamira
RuntimeDirectoryMode=0750
%[5]sRestart=on-failure
RestartSec=3

[Install]
WantedBy=multi-user.target
`, cfg.Router.Service, User, BinPath, o.ConfigPath, bindCap(cfg.Listen))
	if err := os.WriteFile(UnitPath, []byte(unit), 0o644); err != nil {
		return err
	}
	if err := run("systemctl", "daemon-reload"); err != nil {
		return err
	}
	if err := run("systemctl", "enable", "llamira.service"); err != nil {
		return err
	}
	if err := run("systemctl", "restart", "llamira.service"); err != nil {
		return err
	}
	o.logf("✓ llamira.service enabled and (re)started")
	if len(LoadPowerLimits()) > 0 {
		_ = ensurePowerUnit(true)
	}
	if cfg.Listen != "" && !config.IsLoopback(cfg.Listen) {
		_, port, _ := net.SplitHostPort(cfg.Listen)
		host := firstIP()
		if port != "80" {
			host += ":" + port
		}
		scheme := "http"
		if cfg.TLSEnabled() {
			scheme = "https"
		}
		if cfg.AllowUnauthenticated {
			o.logf("\nUI on the LAN: %s://%s/  (no authentication: anyone who can reach it has full access)", scheme, host)
		} else {
			o.logf("\nUI on the LAN: %s://%s/?token=%s", scheme, host, cfg.AuthToken)
			o.logf("(open it once with the token; a cookie keeps you signed in. Token: auth_token in %s)", o.ConfigPath)
			if cfg.ReadToken != "" {
				o.logf("Read-only (dashboards, no changes): %s://%s/?token=%s", scheme, host, cfg.ReadToken)
			}
		}
		if cfg.TLS == "self-signed" {
			o.logf("The certificate is self-signed: the browser warns once. Its SHA-256 fingerprint is in `journalctl -u llamira`.")
		} else if !cfg.TLSEnabled() && !cfg.AllowUnauthenticated {
			o.logf("Plain HTTP: the token travels unencrypted. Use --tls self-signed (or a TLS reverse proxy) on untrusted networks.")
		}
	}
	return nil
}

// bindCap lets the non-root service user bind a privileged port (e.g. :80), and nothing else.
// No CapabilityBoundingSet: that would also strip the capabilities sudo needs.
func bindCap(listen string) string {
	_, p, err := net.SplitHostPort(listen)
	if n, _ := strconv.Atoi(p); err == nil && n > 0 && n < 1024 {
		return "AmbientCapabilities=CAP_NET_BIND_SERVICE\n"
	}
	return ""
}

func randomToken() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// firstIP returns the host's first non-loopback IPv4 address (for printing a URL).
func firstIP() string {
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && !ipn.IP.IsLoopback() && ipn.IP.To4() != nil {
			return ipn.IP.String()
		}
	}
	h, _ := os.Hostname()
	return h
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".new"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// routerFlags stay on the llama-server command line; everything else moves to [*].
var routerFlags = []string{
	"host", "port", "models-dir", "models-max", "models-preset", "models-autoload", "no-models-autoload",
	"api-key", "api-key-file", "ssl-key-file", "ssl-cert-file", "path", "api-prefix",
	"webui", "no-webui", "ui", "no-ui", "webui-config", "webui-config-file", "ui-config", "ui-config-file",
	"metrics", "props", "slots", "no-slots", "threads-http", "timeout", "to",
	"log-file", "log-colors", "log-verbosity", "verbosity", "lv", "log-prefix", "log-timestamps",
	"sleep-idle-seconds", "media-path", "slot-save-path",
}

// Adopt moves per-model flags from the router's ExecStart into the [*] preset section and
// points the router at the preset file via a systemd drop-in.
func Adopt(ctx context.Context, cfg config.Server, o Options) error {
	unit := cfg.Router.Service
	props, err := system.Show(ctx, unit, "ExecStart", "DropInPaths", "FragmentPath")
	if err != nil {
		return err
	}
	argv := strings.Fields(execArgv(props["ExecStart"]))
	if len(argv) == 0 {
		return fmt.Errorf("cannot read ExecStart of %s", unit)
	}
	dropInDir := props["FragmentPath"] + ".d"
	if props["FragmentPath"] == "" {
		dropInDir = "/etc/systemd/system/" + unit + ".d"
	}
	dropIn := filepath.Join(dropInDir, DropInName)

	cat, err := (&llamacpp.CatalogCache{Binary: argv[0]}).Get(ctx)
	if err != nil {
		return err
	}
	var keep []string // router-level args
	var moved []presets.KV
	hasPreset := false
	for i := 1; i < len(argv); i++ {
		name := strings.TrimLeft(argv[i], "-")
		val := ""
		if k, v, ok := strings.Cut(name, "="); ok {
			name, val = k, v
		}
		a, known := cat.Lookup(name)
		takesValue := known && !a.IsFlag()
		if takesValue && val == "" && i+1 < len(argv) {
			i++
			val = argv[i]
		}
		if !known {
			return fmt.Errorf("unknown argument %q in ExecStart; adopt manually", argv[i])
		}
		if a.Key == "models-preset" {
			hasPreset = true
		}
		if slices.Contains(routerFlags, name) || slices.Contains(routerFlags, a.Key) {
			keep = append(keep, "--"+name)
			if takesValue {
				keep = append(keep, val)
			}
			continue
		}
		if !takesValue {
			val = "true"
			if slices.Contains(a.NegNames, name) {
				val = "false"
			}
		}
		moved = append(moved, presets.KV{Key: a.Key, Value: val})
	}
	if hasPreset && len(moved) == 0 {
		o.logf("✓ router already adopted (%s uses --models-preset, no per-model flags on the command line)", unit)
		return nil
	}
	if !hasPreset {
		keep = append(keep, "--models-preset", cfg.PresetFile)
	}

	// Preset file: merge moved flags into [*] (existing [*] values win).
	cur, _ := os.ReadFile(cfg.PresetFile)
	d := presets.Parse(string(cur))
	if _, ok := d.Preamble.Get("version"); !ok {
		d.SetPreamble("version", "1")
	}
	var global []presets.KV
	if s := d.Section(presets.GlobalSection); s != nil {
		global = s.Entries()
	}
	for _, kv := range moved {
		exists := slices.ContainsFunc(global, func(g presets.KV) bool { return cat.Canonical(g.Key) == kv.Key })
		if !exists {
			global = append(global, kv)
		}
	}
	d.Upsert(presets.GlobalSection, global, cat.Canonical)
	newPreset := d.String()
	execStart := argv[0] + " " + strings.Join(keep, " ")
	dropInText := fmt.Sprintf(`# Managed by llamira. Delete this file + "systemctl daemon-reload" to revert.
# Per-model settings (ctx-size, GPUs, ...) live in the preset file, not here:
# command-line flags take precedence over presets in llama.cpp.
[Service]
ExecStart=
ExecStart=%s
`, execStart)

	o.logf("\nAdopt plan for %s:", unit)
	o.logf("  preset file %s:\n%s", cfg.PresetFile, indent(presets.Unified(presets.Diff(string(cur), newPreset), 3)))
	o.logf("  drop-in %s:\n%s", dropIn, indent(dropInText))
	o.logf("  then: systemctl daemon-reload && systemctl restart %s (loaded models are unloaded once)", unit)
	if !o.Yes && (o.Confirm == nil || !o.Confirm("Proceed?")) {
		return errors.New("adopt cancelled")
	}

	router := llamacpp.NewClient(cfg.Router.URL, cfg.Router.APIKey)
	beforeIDs := modelIDs(ctx, router)
	prevDropIn, prevErr := os.ReadFile(dropIn)
	if err := apply.WriteAtomic(cfg.PresetFile, newPreset); err != nil {
		return err
	}
	os.MkdirAll(dropInDir, 0o755)
	if err := os.WriteFile(dropIn, []byte(dropInText), 0o644); err != nil {
		return err
	}
	revert := func(cause error) error {
		if prevErr == nil {
			os.WriteFile(dropIn, prevDropIn, 0o644)
		} else {
			os.Remove(dropIn)
		}
		run("systemctl", "daemon-reload")
		run("systemctl", "restart", unit)
		return fmt.Errorf("adopt failed, drop-in reverted: %w", cause)
	}
	if err := run("systemctl", "daemon-reload"); err != nil {
		return revert(err)
	}
	if err := run("systemctl", "restart", unit); err != nil {
		return revert(err)
	}
	deadline := time.Now().Add(60 * time.Second)
	for router.Health(ctx) != nil {
		if time.Now().After(deadline) {
			return revert(errors.New("router not healthy after restart"))
		}
		time.Sleep(500 * time.Millisecond)
	}
	after := modelIDs(ctx, router)
	for _, id := range beforeIDs {
		if !slices.Contains(after, id) {
			return revert(fmt.Errorf("model %s disappeared", id))
		}
	}
	o.logf("✓ router adopted; %d models available", len(after))
	return nil
}

func modelIDs(ctx context.Context, c *llamacpp.Client) []string {
	ms, _ := c.Models(ctx, false)
	var ids []string
	for _, m := range ms {
		ids = append(ids, m.ID)
	}
	return ids
}

func execArgv(s string) string {
	_, rest, ok := strings.Cut(s, "argv[]=")
	if !ok {
		return s
	}
	argv, _, _ := strings.Cut(rest, " ;")
	return argv
}

func indent(s string) string {
	return "    " + strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "\n    ")
}
