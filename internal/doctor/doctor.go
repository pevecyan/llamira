// Package doctor checks a llamira host for the setup mistakes that break things quietly
// (per-model flags on the router command line, a missing preset flag, an open router, …)
// and says how to fix each one. The checks are pure functions of a gathered Input.
package doctor

import (
	"fmt"
	"net"
	"regexp"
	"slices"
	"strings"
)

// Check is one finding.
type Check struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"` // ok, warn, fail
	Detail string `json:"detail,omitempty"`
	Fix    string `json:"fix,omitempty"`
}

const (
	OK   = "ok"
	Warn = "warn"
	Fail = "fail"
)

// Issue is a preset validation finding (apply.Issue without the import cycle).
type Issue struct {
	Severity string // error, warning
	Section  string
	Message  string
}

// Input is everything the checks look at. Zero values mean "not available".
type Input struct {
	PresetFile string // config preset_file
	Service    string // router unit
	CacheDir   string // config router.cache_dir
	HFToken    bool   // config hf_token is set

	RouterErr  error  // router /health
	RouterRole string // /props role ("router" in router mode)

	ExecStart   []string // router argv (from systemd); nil = unknown
	ExecErr     error
	UnitEnv     []string // Environment= of the router unit
	Canonical   func(string) string
	PresetErr   error   // reading the preset file
	ValidateErr error   // the validation itself could not run (e.g. no llama-server binary)
	Issues      []Issue // static validation of the preset file
	FailedIDs   []string
	DiskPath    string
	DiskFree    uint64
	DiskErr     error
	DiskLowGB   int // alert threshold (0 = off)
	GPUCount    int
	GPUErr      error
	JournalErr  error

	Listen               string // UI address ("" = off)
	ListenAuth           bool   // auth_token or password_hash set
	AllowUnauthenticated bool
	TLS                  bool
}

// routerFlags are router-level options: they belong on the command line. Everything else
// on the command line overrides the same key in every preset.
var routerFlags = []string{
	"host", "port", "models-dir", "models-preset", "models-max", "models-autoload", "no-models-autoload",
	"api-key", "api-key-file", "ssl-key-file", "ssl-cert-file", "timeout", "threads-http", "metrics",
	"path", "api-prefix", "sleep-idle-seconds", "log-disable", "log-file", "log-colors", "log-verbose",
	"verbose", "verbosity", "log-verbosity", "log-prefix", "no-log-prefix", "log-timestamps", "no-log-timestamps",
	"log-jsonl", "no-log-jsonl", "log-prompts-dir", "webui", "no-webui", "ui", "no-ui", "webui-config",
	"webui-config-file", "ui-config", "ui-config-file", "webui-mcp-proxy", "no-webui-mcp-proxy", "ui-mcp-proxy",
	"no-ui-mcp-proxy", "offline", "props", "slots", "no-slots", "cors",
}

var reFlag = regexp.MustCompile(`^--?[A-Za-z]`)

// Flags parses argv[1:] into option name → value ("" for switches).
func Flags(argv []string) map[string]string {
	out := map[string]string{}
	for i := 1; i < len(argv); i++ {
		if !reFlag.MatchString(argv[i]) {
			continue
		}
		name, val, hasEq := strings.Cut(strings.TrimLeft(argv[i], "-"), "=")
		if !hasEq && i+1 < len(argv) && !reFlag.MatchString(argv[i+1]) {
			val = argv[i+1]
			i++
		}
		out[name] = val
	}
	return out
}

func envValue(env []string, key string) (string, bool) {
	for _, e := range env {
		if k, v, ok := strings.Cut(e, "="); ok && k == key {
			return v, true
		}
	}
	return "", false
}

// Run evaluates all checks.
func Run(in Input) []Check {
	var out []Check
	add := func(c Check) { out = append(out, c) }
	canon := in.Canonical
	if canon == nil {
		canon = func(s string) string { return s }
	}

	// router
	switch {
	case in.RouterErr != nil:
		add(Check{ID: "router", Title: "Router reachable", Status: Fail, Detail: in.RouterErr.Error(),
			Fix: fmt.Sprintf("Check `systemctl status %s` and `llamira logs` on the host.", in.Service)})
	case in.RouterRole != "" && in.RouterRole != "router":
		add(Check{ID: "router", Title: "Router reachable", Status: Fail,
			Detail: fmt.Sprintf("llama-server runs in %q mode, not as a router", in.RouterRole),
			Fix:    "Start llama-server without -m and with --models-preset (run `llamira install --adopt`)."})
	default:
		add(Check{ID: "router", Title: "Router reachable", Status: OK})
	}

	// command line
	if in.ExecStart == nil {
		detail := "the router's command line is unknown"
		if in.ExecErr != nil {
			detail = in.ExecErr.Error()
		}
		add(Check{ID: "cmdline", Title: "Router command line", Status: Warn, Detail: detail})
	} else {
		flags := Flags(in.ExecStart)
		canonFlags := map[string]string{}
		for k, v := range flags {
			canonFlags[canon(k)] = v
		}
		preset, hasPreset := canonFlags["models-preset"]
		switch {
		case !hasPreset:
			add(Check{ID: "preset-flag", Title: "Router reads the preset file", Status: Fail,
				Detail: "--models-preset is not on the router command line, so presets have no effect",
				Fix:    "Run `llamira install --adopt`, or add --models-preset " + in.PresetFile + " to the drop-in."})
		case in.PresetFile != "" && preset != in.PresetFile:
			add(Check{ID: "preset-flag", Title: "Router reads the preset file", Status: Fail,
				Detail: fmt.Sprintf("the router reads %s, llamira edits %s", preset, in.PresetFile),
				Fix:    "Point --models-preset and preset_file in /etc/llamira/config.yaml at the same file."})
		default:
			add(Check{ID: "preset-flag", Title: "Router reads the preset file", Status: OK, Detail: preset})
		}

		var overrides []string
		for k := range flags {
			c := canon(k)
			if !slices.Contains(routerFlags, c) && !slices.Contains(routerFlags, k) {
				overrides = append(overrides, "--"+k)
			}
		}
		slices.Sort(overrides)
		if len(overrides) > 0 {
			add(Check{ID: "cli-overrides", Title: "No per-model flags on the command line", Status: Warn,
				Detail: strings.Join(overrides, " ") + " override the same setting in every preset",
				Fix:    "Move them into the [*] section of the preset file and remove them from the drop-in (`llamira install --adopt` does this)."})
		} else {
			add(Check{ID: "cli-overrides", Title: "No per-model flags on the command line", Status: OK})
		}

		host := canonFlags["host"]
		_, hasKey := canonFlags["api-key"]
		_, hasKeyFile := canonFlags["api-key-file"]
		if host != "" && !isLoopbackHost(host) && !hasKey && !hasKeyFile {
			add(Check{ID: "router-key", Title: "Router API key", Status: Warn,
				Detail: fmt.Sprintf("the router listens on %s without an API key: anyone on the network can load, download and delete models", host),
				Fix:    "Turn on Require API key on the llama.cpp page (then give the key to LiteLLM/OpenWebUI)."})
		} else {
			add(Check{ID: "router-key", Title: "Router API key", Status: OK})
		}
	}

	// environment
	if in.ExecStart != nil {
		_, hasHF := envValue(in.UnitEnv, "HF_TOKEN")
		switch {
		case in.HFToken && !hasHF:
			add(Check{ID: "hf-token", Title: "Hugging Face token for downloads", Status: Warn,
				Detail: "hf_token is set for llamira, but the router has no HF_TOKEN: downloads of gated or private repos fail",
				Fix:    "Add Environment=HF_TOKEN=... to the router drop-in (or ignore this if it comes from an EnvironmentFile)."})
		default:
			add(Check{ID: "hf-token", Title: "Hugging Face token for downloads", Status: OK})
		}
		cache, hasCache := envValue(in.UnitEnv, "LLAMA_CACHE")
		switch {
		case in.CacheDir != "" && hasCache && strings.TrimRight(cache, "/") != strings.TrimRight(in.CacheDir, "/"):
			add(Check{ID: "cache-dir", Title: "Download cache", Status: Warn,
				Detail: fmt.Sprintf("the router downloads into %s, llamira scans %s", cache, in.CacheDir),
				Fix:    "Remove router.cache_dir from the config (it then follows LLAMA_CACHE), or make them equal."})
		case !hasCache && in.CacheDir == "":
			add(Check{ID: "cache-dir", Title: "Download cache", Status: Warn,
				Detail: "LLAMA_CACHE is not set on the router: downloads go to the service user's ~/.cache",
				Fix:    "Add Environment=LLAMA_CACHE=/models/cache to the router unit."})
		default:
			add(Check{ID: "cache-dir", Title: "Download cache", Status: OK, Detail: firstNonEmpty(cache, in.CacheDir)})
		}
	}

	// preset file
	switch {
	case in.PresetErr != nil:
		add(Check{ID: "preset-file", Title: "Preset file is valid", Status: Fail, Detail: in.PresetErr.Error(),
			Fix: "Restore a backup on Presets → Backups."})
	case in.ValidateErr != nil:
		add(Check{ID: "preset-file", Title: "Preset file is valid", Status: Warn, Detail: "could not validate: " + in.ValidateErr.Error()})
	default:
		var errs, warns []string
		for _, i := range in.Issues {
			msg := i.Message
			if i.Section != "" {
				msg = "[" + i.Section + "] " + msg
			}
			if i.Severity == "error" {
				errs = append(errs, msg)
			} else {
				warns = append(warns, msg)
			}
		}
		switch {
		case len(errs) > 0:
			add(Check{ID: "preset-file", Title: "Preset file is valid", Status: Fail, Detail: strings.Join(errs, "; "),
				Fix: "Fix the sections on the Presets page; a restart with this file would fail."})
		case len(warns) > 0:
			add(Check{ID: "preset-file", Title: "Preset file is valid", Status: Warn, Detail: strings.Join(warns, "; ")})
		default:
			add(Check{ID: "preset-file", Title: "Preset file is valid", Status: OK})
		}
	}
	if len(in.FailedIDs) > 0 {
		add(Check{ID: "models-failed", Title: "Models load", Status: Warn,
			Detail: "failed to load: " + strings.Join(in.FailedIDs, ", "),
			Fix:    "See `llamira logs`; a VRAM estimate on the Presets page shows whether the settings fit."})
	} else {
		add(Check{ID: "models-failed", Title: "Models load", Status: OK})
	}

	// resources
	switch {
	case in.DiskErr != nil:
		add(Check{ID: "disk", Title: "Disk space", Status: Warn, Detail: in.DiskErr.Error()})
	case in.DiskFree < 1<<30:
		add(Check{ID: "disk", Title: "Disk space", Status: Fail, Detail: fmt.Sprintf("%.2f GB free on %s", gb(in.DiskFree), in.DiskPath),
			Fix: "Delete unused models (Usage → Cleanup)."})
	case in.DiskLowGB > 0 && in.DiskFree < uint64(in.DiskLowGB)<<30:
		add(Check{ID: "disk", Title: "Disk space", Status: Warn, Detail: fmt.Sprintf("%.1f GB free on %s", gb(in.DiskFree), in.DiskPath),
			Fix: "Delete unused models (Usage → Cleanup)."})
	default:
		add(Check{ID: "disk", Title: "Disk space", Status: OK, Detail: fmt.Sprintf("%.0f GB free on %s", gb(in.DiskFree), in.DiskPath)})
	}
	switch {
	case in.GPUErr != nil:
		add(Check{ID: "gpus", Title: "GPUs visible", Status: Warn, Detail: in.GPUErr.Error(),
			Fix: "GPU cards, VRAM estimates and power limits need nvidia-smi (NVIDIA) or the amdgpu driver (AMD)."})
	case in.GPUCount == 0:
		add(Check{ID: "gpus", Title: "GPUs visible", Status: Warn, Detail: "no GPU found: models run on the CPU"})
	default:
		add(Check{ID: "gpus", Title: "GPUs visible", Status: OK, Detail: fmt.Sprintf("%d GPU(s)", in.GPUCount)})
	}
	if in.JournalErr != nil {
		add(Check{ID: "journal", Title: "Router journal readable", Status: Warn, Detail: in.JournalErr.Error(),
			Fix: "Logs and usage statistics need the llamira user in the systemd-journal group."})
	} else {
		add(Check{ID: "journal", Title: "Router journal readable", Status: OK})
	}

	// llamira's own UI
	if in.Listen != "" && !isLoopbackAddr(in.Listen) {
		switch {
		case in.AllowUnauthenticated:
			add(Check{ID: "ui-auth", Title: "UI access", Status: Warn,
				Detail: fmt.Sprintf("the UI on %s has no authentication: anyone on the network has full control", in.Listen),
				Fix:    "Set auth_token in /etc/llamira/config.yaml (or deploy with --auth token)."})
		case !in.TLS:
			add(Check{ID: "ui-auth", Title: "UI access", Status: Warn,
				Detail: fmt.Sprintf("the UI on %s uses plain HTTP: the login token or password is sent unencrypted", in.Listen),
				Fix:    "Set tls: self-signed (or tls_cert/tls_key) in /etc/llamira/config.yaml, or put a TLS reverse proxy in front."})
		default:
			add(Check{ID: "ui-auth", Title: "UI access", Status: OK, Detail: "token + TLS"})
		}
	}
	return out
}

// Worst returns the most severe status of the checks.
func Worst(cs []Check) string {
	w := OK
	for _, c := range cs {
		if c.Status == Fail {
			return Fail
		}
		if c.Status == Warn {
			w = Warn
		}
	}
	return w
}

func isLoopbackHost(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func isLoopbackAddr(addr string) bool {
	h, _, err := net.SplitHostPort(addr)
	return err == nil && isLoopbackHost(h)
}

func gb(n uint64) float64 { return float64(n) / (1 << 30) }

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}
