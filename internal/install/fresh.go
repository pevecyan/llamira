package install

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"llamira/internal/config"
	"llamira/internal/llamacpp"
)

// Manifest records what an install created, so uninstall can remove exactly that.
type Manifest struct {
	Fresh     bool      `json:"fresh"` // llama.cpp itself was installed by llamira
	Created   time.Time `json:"created"`
	Packages  []string  `json:"packages,omitempty"`
	Units     []string  `json:"units,omitempty"`
	Dirs      []string  `json:"dirs,omitempty"`
	LlamaRef  string    `json:"llama_ref,omitempty"`
	LlamaFrom string    `json:"llama_from,omitempty"` // prebuilt <flavor> | source
	ModelsDir string    `json:"models_dir,omitempty"`
}

const manifestFile = "install-manifest.json"

func manifestPath(cfg config.Server) string { return filepath.Join(cfg.DataDir, manifestFile) }

func loadManifest(cfg config.Server) Manifest {
	var m Manifest
	if b, err := os.ReadFile(manifestPath(cfg)); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

func saveManifest(cfg config.Server, m Manifest) {
	_ = os.MkdirAll(cfg.DataDir, 0o755)
	b, _ := json.MarshalIndent(m, "", "  ")
	_ = os.WriteFile(manifestPath(cfg), b, 0o644)
}

func addUnique(list []string, v ...string) []string {
	for _, x := range v {
		if !slices.Contains(list, x) {
			list = append(list, x)
		}
	}
	return list
}

// routerUnitExists reports whether a llama-server unit is installed.
func routerUnitExists(unit string) bool {
	out, _ := exec.Command("systemctl", "list-unit-files", unit, "--no-legend").Output()
	return strings.TrimSpace(string(out)) != ""
}

// FreshInstall installs llama.cpp and a router service on a host that has none. It is
// idempotent: finished steps are skipped when run again.
func FreshInstall(ctx context.Context, cfg *config.Server, o Options) error {
	h := llamacpp.DetectHost(ctx, "/")
	method := o.LlamaFrom
	if method == "" {
		method = "prebuilt"
	}
	o.logf("Fresh install of llama.cpp:")
	o.logf("  host: %s %s, %d GB RAM, %d GB free disk", h.OS, h.Arch, h.RAMGB, h.DiskFreeGB)
	if len(h.GPUs) > 0 {
		o.logf("  GPUs: %s (compute %s), driver %s, CUDA %s, nvcc %s", strings.Join(h.GPUs, ", "),
			strings.Join(h.ComputeCaps, ","), h.Driver, h.CUDA, orNone(h.NVCC))
	} else {
		o.logf("  no NVIDIA GPU found (CPU build%s)", map[bool]string{true: "; AMD GPU detected", false: ""}[h.AMD])
	}
	if h.Arch == "" {
		return fmt.Errorf("unsupported CPU architecture")
	}
	if method == "source" && len(h.GPUs) > 0 && h.NVCC == "" {
		return errors.New("building with CUDA needs the CUDA toolkit (nvcc). Install it first, e.g. on Ubuntu: " +
			"https://developer.nvidia.com/cuda-downloads (or use the prebuilt build: --llama-cpp prebuilt)")
	}
	ref := o.LlamaRef
	if ref == "" {
		rels, err := llamacpp.Releases(ctx)
		if err != nil {
			return fmt.Errorf("find the latest llama.cpp release: %w", err)
		}
		ref = rels[0].Tag
	}
	o.logf("  llama.cpp %s, %s (%s)", ref, method, map[string]string{"prebuilt": "flavour " + h.Flavor, "source": "cmake build"}[method])
	o.logf("  router: llama-server.service on :8080, models in %s, presets in %s", cfg.Router.ModelsDir, cfg.PresetFile)
	if !o.Yes && (o.Confirm == nil || !o.Confirm("Proceed with the fresh install?")) {
		return errors.New("fresh install cancelled")
	}
	m := loadManifest(*cfg)
	m.Fresh, m.LlamaRef, m.ModelsDir = true, ref, cfg.Router.ModelsDir
	if m.Created.IsZero() {
		m.Created = time.Now()
	}
	defer func() { saveManifest(*cfg, m) }()

	// 1. packages
	pkgs := []string{"ca-certificates", "curl", "dbus", "libgomp1", "libssl3t64", "zlib1g", "libzstd1", "libbrotli1"}
	if h.Flavor == "vulkan" {
		pkgs = append(pkgs, "libvulkan1")
	}
	if method == "source" {
		pkgs = append(pkgs, "git", "cmake", "build-essential", "libssl-dev")
	}
	installed, err := installPackages(h, pkgs, o)
	if err != nil {
		return err
	}
	m.Packages = addUnique(m.Packages, installed...)

	// 2. llama.cpp into the builds dir (same layout as updater builds)
	builds := buildsDir(*cfg)
	bin := filepath.Join(builds, ref, "build", "bin", "llama-server")
	if _, err := os.Stat(bin); err != nil {
		if method == "prebuilt" {
			err = llamacpp.FetchPrebuilt(ctx, llamacpp.FetchOptions{BuildsDir: builds, Ref: ref, Flavor: h.Flavor, Arch: h.Arch, Out: o.Out})
			m.LlamaFrom = "prebuilt " + h.Flavor
		} else {
			src := cfg.Router.WorkDir
			if _, e := os.Stat(filepath.Join(src, ".git")); e != nil {
				if e := run("git", "clone", "https://github.com/ggml-org/llama.cpp", src); e != nil {
					return e
				}
				m.Dirs = addUnique(m.Dirs, src)
			}
			err = llamacpp.RunBuild(ctx, llamacpp.BuildOptions{SrcDir: src, BuildsDir: builds, Ref: ref,
				Flags: llamacpp.FreshFlags(h), Out: o.Out})
			m.LlamaFrom = "source"
		}
		if err != nil {
			return err
		}
		m.Dirs = addUnique(m.Dirs, builds)
	} else {
		o.logf("✓ llama.cpp %s already present", ref)
	}

	// 3. directories and a preset file with defaults for this host
	cache := filepath.Join(cfg.Router.ModelsDir, "cache")
	for _, d := range []string{cfg.Router.ModelsDir, cache, filepath.Dir(cfg.PresetFile)} {
		if _, err := os.Stat(d); errors.Is(err, os.ErrNotExist) {
			if err := os.MkdirAll(d, 0o755); err != nil {
				return err
			}
			if d != filepath.Dir(cfg.PresetFile) {
				m.Dirs = addUnique(m.Dirs, d)
			}
		}
	}
	if _, err := os.Stat(cfg.PresetFile); errors.Is(err, os.ErrNotExist) {
		ctxSize, ngl := "32768", "all"
		if len(h.GPUs) == 0 {
			ctxSize, ngl = "8192", "0"
		}
		preset := fmt.Sprintf("version = 1\n\n; Global defaults for every model (managed by llamira).\n[*]\nctx-size = %s\nn-gpu-layers = %s\nflash-attn = auto\n", ctxSize, ngl)
		if len(h.GPUs) > 1 {
			preset += "split-mode = layer\n"
		}
		if err := os.WriteFile(cfg.PresetFile, []byte(preset), 0o644); err != nil {
			return err
		}
		o.logf("✓ wrote preset file %s", cfg.PresetFile)
	}

	// 4. router unit (base) + llamira drop-in (holds the command line, like adopted hosts)
	unitPath := "/etc/systemd/system/" + cfg.Router.Service
	if !routerUnitExists(cfg.Router.Service) {
		base := fmt.Sprintf(`[Unit]
Description=llama.cpp router (installed by llamira)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=%[1]s
Environment=LLAMA_CACHE=%[2]s
ExecStart=%[3]s --host 0.0.0.0 --port 8080
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
`, cfg.DataDir, cache, bin)
		if err := os.WriteFile(unitPath, []byte(base), 0o644); err != nil {
			return err
		}
		m.Units = addUnique(m.Units, unitPath)
	}
	dropDir := unitPath + ".d"
	_ = os.MkdirAll(dropDir, 0o755)
	dropIn := filepath.Join(dropDir, DropInName)
	if _, err := os.Stat(dropIn); errors.Is(err, os.ErrNotExist) {
		text := fmt.Sprintf(`# Managed by llamira. Per-model settings live in the preset file.
[Service]
ExecStart=
ExecStart=%s --host 0.0.0.0 --port 8080 --models-dir %s --models-max 1 --models-preset %s
`, bin, cfg.Router.ModelsDir, cfg.PresetFile)
		if err := os.WriteFile(dropIn, []byte(text), 0o644); err != nil {
			return err
		}
		m.Units = addUnique(m.Units, dropIn)
	}
	cfg.Router.Binary = bin
	cfg.Router.WorkDir = cfg.DataDir

	// 5. start and verify
	if err := run("systemctl", "daemon-reload"); err != nil {
		return err
	}
	if err := run("systemctl", "enable", "--now", cfg.Router.Service); err != nil {
		return err
	}
	router := llamacpp.NewClient(cfg.Router.URL, "")
	deadline := time.Now().Add(2 * time.Minute)
	for router.Health(ctx) != nil {
		if time.Now().After(deadline) {
			return errors.New("llama-server did not become healthy; see journalctl -u " + cfg.Router.Service)
		}
		time.Sleep(time.Second)
	}
	if _, err := router.Models(ctx, false); err != nil {
		return fmt.Errorf("router /models: %w", err)
	}
	o.logf("✓ llama-server is running (router on :8080, %s)", ref)
	return nil
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// installPackages installs missing packages (apt only for now); returns the ones it installed.
func installPackages(h llamacpp.Host, pkgs []string, o Options) ([]string, error) {
	if h.PkgManager != "apt-get" {
		o.logf("  (package manager %q: make sure these are installed: %s)", h.PkgManager, strings.Join(pkgs, " "))
		return nil, nil
	}
	var missing []string
	for _, p := range pkgs {
		if exec.Command("dpkg-query", "-W", "-f=${Status}", p).Run() != nil {
			missing = append(missing, p)
		} else if out, _ := exec.Command("dpkg-query", "-W", "-f=${Status}", p).Output(); !strings.Contains(string(out), "install ok installed") {
			missing = append(missing, p)
		}
	}
	if len(missing) == 0 {
		return nil, nil
	}
	o.logf("  installing packages: %s", strings.Join(missing, " "))
	cmd := exec.Command("apt-get", "install", "-y", "--no-install-recommends")
	cmd.Args = append(cmd.Args, missing...)
	cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
	if out, err := cmd.CombinedOutput(); err != nil {
		// package lists may be missing in minimal images
		if e := run("apt-get", "update"); e == nil {
			cmd = exec.Command("apt-get", append([]string{"install", "-y", "--no-install-recommends"}, missing...)...)
			cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
			out, err = cmd.CombinedOutput()
		}
		if err != nil {
			return nil, fmt.Errorf("apt-get install: %v: %s", err, lastLines(string(out), 5))
		}
	}
	return missing, nil
}

func lastLines(s string, n int) string {
	l := strings.Split(strings.TrimSpace(s), "\n")
	if len(l) > n {
		l = l[len(l)-n:]
	}
	return strings.Join(l, "\n")
}

// Uninstall removes llamira from the host. Adopted routers get their original command line
// back; routers installed by llamira are removed. Models stay unless purgeModels.
func Uninstall(ctx context.Context, cfgPath string, purgeModels, removePackages bool, o Options) error {
	if os.Geteuid() != 0 {
		return errors.New("uninstall must run as root")
	}
	cfg, err := config.LoadServer(cfgPath)
	if err != nil {
		return err
	}
	m := loadManifest(cfg)
	o.logf("Uninstall llamira from this host:")
	isModels := func(d string) bool {
		return d == cfg.Router.ModelsDir || strings.HasPrefix(d, cfg.Router.ModelsDir+"/")
	}
	if m.Fresh {
		o.logf("  remove llama-server (installed by llamira): %s", strings.Join(m.Units, ", "))
		var dirs []string
		for _, d := range m.Dirs {
			if !isModels(d) {
				dirs = append(dirs, d)
			}
		}
		o.logf("  remove directories: %s", strings.Join(dirs, ", "))
	} else {
		o.logf("  restore the original llama-server command line (remove the %s drop-in) and restart it", DropInName)
	}
	o.logf("  remove llamira.service, sudoers rule, %s, %s, user %s and %s", "/etc/llamira", cfg.DataDir, User, BinPath)
	if purgeModels {
		o.logf("  DELETE models in %s", cfg.Router.ModelsDir)
	} else {
		o.logf("  keep models in %s", cfg.Router.ModelsDir)
	}
	if !o.Yes && (o.Confirm == nil || !o.Confirm("Proceed?")) {
		return errors.New("uninstall cancelled")
	}
	for _, u := range []string{"llamira.service", "llamira-power.service"} {
		_ = run("systemctl", "disable", "--now", u)
	}
	_ = os.Remove(UnitPath)
	_ = os.Remove(PowerUnit)
	props, _ := exec.Command("systemctl", "show", cfg.Router.Service, "-p", "FragmentPath", "--value").Output()
	frag := strings.TrimSpace(string(props))
	if m.Fresh {
		_ = run("systemctl", "disable", "--now", cfg.Router.Service)
		for _, u := range m.Units {
			_ = os.Remove(u)
		}
		if frag != "" {
			_ = os.Remove(frag + ".d") // only succeeds when empty
		}
		for _, d := range m.Dirs {
			if isModels(d) {
				continue // models handled below
			}
			_ = os.RemoveAll(d)
		}
	} else if frag != "" {
		_ = os.Remove(filepath.Join(frag+".d", DropInName))
		_ = run("systemctl", "daemon-reload")
		_ = run("systemctl", "restart", cfg.Router.Service)
	}
	_ = run("systemctl", "daemon-reload")
	_ = os.Remove(Sudoers)
	if purgeModels && cfg.Router.ModelsDir != "" && cfg.Router.ModelsDir != "/" {
		_ = os.RemoveAll(cfg.Router.ModelsDir)
	}
	if removePackages && len(m.Packages) > 0 {
		args := append([]string{"remove", "-y"}, m.Packages...)
		_ = run("apt-get", args...)
	}
	_ = run("userdel", User)
	_ = os.RemoveAll(cfg.DataDir)
	_ = os.RemoveAll(filepath.Dir(cfgPath))
	_ = os.Remove(BinPath)
	o.logf("✓ llamira removed")
	return nil
}
