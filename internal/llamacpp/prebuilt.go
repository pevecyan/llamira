package llamacpp

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Host describes what a llama.cpp install needs to know about the machine.
type Host struct {
	OS          string   `json:"os"`    // e.g. "ubuntu 24.04"
	OSID        string   `json:"os_id"` // ubuntu, debian, arch, fedora…
	PkgManager  string   `json:"pkg_manager"`
	Arch        string   `json:"arch"` // x64, arm64
	GPUs        []string `json:"gpus"` // names
	ComputeCaps []string `json:"compute_caps"`
	Driver      string   `json:"driver,omitempty"`
	CUDA        string   `json:"cuda,omitempty"` // highest CUDA version the driver supports
	NVCC        string   `json:"nvcc,omitempty"`
	AMD         bool     `json:"amd"`
	DiskFreeGB  int      `json:"disk_free_gb"`
	RAMGB       int      `json:"ram_gb"`
	Flavor      string   `json:"flavor"` // prebuilt flavour: cuda-13.4, cuda-12.8, rocm-10.0, vulkan, cpu
}

var (
	reCUDA = regexp.MustCompile(`CUDA (?:UMD )?[Vv]ersion\s*:\s*([0-9]+\.[0-9]+)`)
	reNVCC = regexp.MustCompile(`release ([0-9]+\.[0-9]+)`)
)

// DetectHost inspects the machine (read-only).
func DetectHost(ctx context.Context, diskPath string) Host {
	h := Host{Arch: map[string]string{"amd64": "x64", "arm64": "arm64"}[runtime.GOARCH]}
	if b, err := os.ReadFile("/etc/os-release"); err == nil {
		kv := map[string]string{}
		for _, l := range strings.Split(string(b), "\n") {
			if k, v, ok := strings.Cut(l, "="); ok {
				kv[k] = strings.Trim(v, `"`)
			}
		}
		h.OSID, h.OS = kv["ID"], strings.TrimSpace(kv["ID"]+" "+kv["VERSION_ID"])
	}
	for _, pm := range []string{"apt-get", "dnf", "pacman", "zypper"} {
		if _, err := exec.LookPath(pm); err == nil {
			h.PkgManager = pm
			break
		}
	}
	run := func(name string, args ...string) string {
		c, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		out, _ := exec.CommandContext(c, name, args...).Output()
		return string(out)
	}
	if out := run("nvidia-smi", "--query-gpu=name,compute_cap,driver_version", "--format=csv,noheader"); out != "" {
		for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
			f := strings.Split(l, ",")
			if len(f) >= 3 {
				h.GPUs = append(h.GPUs, strings.TrimSpace(f[0]))
				h.ComputeCaps = append(h.ComputeCaps, strings.TrimSpace(f[1]))
				h.Driver = strings.TrimSpace(f[2])
			}
		}
		// newer drivers: "CUDA UMD version : 13.4" in --version; older: "CUDA Version: 12.4" in the header
		for _, txt := range []string{run("nvidia-smi", "--version"), run("nvidia-smi")} {
			for _, m := range reCUDA.FindAllStringSubmatch(txt, -1) {
				h.CUDA = m[1]
			}
			if h.CUDA != "" {
				break
			}
		}
	}
	if m := reNVCC.FindStringSubmatch(run("nvcc", "--version") + run("/usr/local/cuda/bin/nvcc", "--version")); m != nil {
		h.NVCC = m[1]
	}
	if _, err := os.Stat("/dev/kfd"); err == nil {
		h.AMD = true
	}
	if diskPath != "" {
		if out := run("df", "-BG", "--output=avail", diskPath); out != "" {
			f := strings.Fields(out)
			h.DiskFreeGB, _ = strconv.Atoi(strings.TrimSuffix(f[len(f)-1], "G"))
		}
	}
	if b, err := os.ReadFile("/proc/meminfo"); err == nil {
		var kb int
		fmt.Sscanf(strings.TrimPrefix(strings.SplitN(string(b), "\n", 2)[0], "MemTotal:"), "%d", &kb)
		h.RAMGB = kb / 1024 / 1024
	}
	h.Flavor = h.prebuiltFlavor()
	return h
}

// prebuiltFlavor picks the upstream Linux release build for this host.
func (h Host) prebuiltFlavor() string {
	if len(h.GPUs) > 0 && h.CUDA != "" {
		major, _ := strconv.Atoi(strings.Split(h.CUDA, ".")[0])
		switch {
		case major >= 13:
			return "cuda-13.4"
		case major == 12 && h.Arch == "x64":
			return "cuda-12.8"
		}
	}
	// AMD: ROCm builds need the ROCm runtime; Vulkan works with the regular Mesa/AMD driver
	if h.AMD && h.Arch == "x64" {
		if _, err := os.Stat("/opt/rocm"); err == nil {
			return "rocm-10.0"
		}
	}
	if icds, _ := filepath.Glob("/usr/share/vulkan/icd.d/*.json"); len(icds) > 0 && (h.AMD || len(h.GPUs) > 0) {
		return "vulkan"
	}
	return "cpu"
}

// CUDAArchitectures turns compute capabilities ("8.6") into CMAKE_CUDA_ARCHITECTURES ("86").
func (h Host) CUDAArchitectures() string {
	seen := map[string]bool{}
	var out []string
	for _, c := range h.ComputeCaps {
		a := strings.ReplaceAll(c, ".", "")
		if a != "" && !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	return strings.Join(out, ";")
}

// PrebuiltAssets returns the release asset names for a tag and flavour.
func PrebuiltAssets(tag, flavor, arch string) []string {
	if flavor == "cpu" || flavor == "" {
		return []string{fmt.Sprintf("llama-%s-bin-ubuntu-%s.tar.gz", tag, arch)}
	}
	assets := []string{fmt.Sprintf("llama-%s-bin-ubuntu-%s-%s.tar.gz", tag, flavor, arch)}
	if strings.HasPrefix(flavor, "cuda-") {
		assets = append(assets, fmt.Sprintf("cudart-llama-%s-bin-ubuntu-%s-%s.tar.gz", tag, flavor, arch))
	}
	return assets
}

// FetchOptions configures FetchPrebuilt.
type FetchOptions struct {
	BuildsDir  string
	Ref        string // release tag, e.g. b11516
	Flavor     string
	Arch       string
	WorkDir    string
	Env        []string
	PresetFile string
	ModelsDir  string
	Out        io.Writer
}

// FetchPrebuilt downloads an upstream release build into <BuildsDir>/<ref>/build/bin (the
// layout of source builds, so switching works the same), then validates it like RunBuild.
func FetchPrebuilt(ctx context.Context, o FetchOptions) (err error) {
	if !reRef.MatchString(o.Ref) || o.Ref == "master" || !strings.HasPrefix(o.Ref, "b") {
		return fmt.Errorf("prebuilt builds need a release tag like b11516, not %q", o.Ref)
	}
	dir := filepath.Join(o.BuildsDir, o.Ref)
	bin := filepath.Join(dir, "build", "bin")
	b := Build{Ref: o.Ref, Dir: dir, Source: "prebuilt " + o.Flavor}
	logf := func(format string, a ...any) { fmt.Fprintf(o.Out, "==> "+format+"\n", a...) }
	defer func() { finishBuild(&b, dir, err, logf) }()

	_ = os.RemoveAll(dir)
	if err := os.MkdirAll(bin, 0o755); err != nil {
		return err
	}
	for _, asset := range PrebuiltAssets(o.Ref, o.Flavor, o.Arch) {
		url := "https://github.com/ggml-org/llama.cpp/releases/download/" + o.Ref + "/" + asset
		logf("downloading %s", url)
		if err := downloadTarGz(ctx, url, bin, o.Out); err != nil {
			return fmt.Errorf("%s: %w", asset, err)
		}
	}
	return validateBuild(ctx, &b, filepath.Join(bin, "llama-server"), o.WorkDir, o.Env, o.PresetFile, o.ModelsDir, logf, o.Out)
}

// downloadTarGz streams a .tar.gz and extracts its regular files and symlinks flat into dst
// (release archives have a single top-level directory).
func downloadTarGz(ctx context.Context, url, dst string, out io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Timeout: 30 * time.Minute}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d (no such build for this platform?)", resp.StatusCode)
	}
	gz, err := gzip.NewReader(&progressReader{r: resp.Body, total: resp.ContentLength, out: out})
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.Base(h.Name)
		if name == "." || name == "/" || strings.Contains(name, "..") {
			continue
		}
		target := filepath.Join(dst, name)
		switch h.Typeflag {
		case tar.TypeReg:
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(h.Mode)&0o755|0o644)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			f.Close()
		case tar.TypeSymlink:
			if strings.Contains(h.Linkname, "/") {
				continue // only same-directory library links are expected
			}
			_ = os.Remove(target)
			if err := os.Symlink(h.Linkname, target); err != nil {
				return err
			}
		}
	}
}

type progressReader struct {
	r       io.Reader
	total   int64
	n       int64
	lastPct int64
	out     io.Writer
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.n += int64(n)
	if p.total > 0 {
		if pct := p.n * 100 / p.total; pct >= p.lastPct+10 {
			p.lastPct = pct - pct%10
			fmt.Fprintf(p.out, "    %d%% of %d MB\n", p.lastPct, p.total>>20)
		}
	}
	return n, err
}

// validateBuild checks that a new llama-server runs and accepts the current preset file.
func validateBuild(ctx context.Context, b *Build, binary, workDir string, env []string, presetFile, modelsDir string,
	logf func(string, ...any), out io.Writer) error {
	o, err := exec.CommandContext(ctx, binary, "--version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("new binary does not run: %v: %s", err, o)
	}
	for _, l := range strings.Split(string(o), "\n") {
		if v, ok := strings.CutPrefix(l, "version:"); ok {
			b.Version = strings.TrimSpace(v)
		}
	}
	if presetFile == "" {
		return nil
	}
	text, err := os.ReadFile(presetFile)
	if os.IsNotExist(err) {
		return nil // fresh install: no preset file yet
	}
	if err != nil {
		return err
	}
	if workDir == "" {
		workDir = filepath.Dir(binary)
	}
	logf("validating: throwaway router with the current preset file on the new binary")
	res, err := DryRun(ctx, DryRunOptions{Binary: binary, WorkDir: workDir, Env: env, ModelsDir: modelsDir}, string(text))
	if err != nil {
		if res != nil {
			fmt.Fprintln(out, res.Log)
		}
		return fmt.Errorf("the new version rejects the current preset file: %w", err)
	}
	b.DryRun = fmt.Sprintf("current preset file accepted, %d models listed", len(res.Models))
	logf("%s", b.DryRun)
	return nil
}

// finishBuild records the outcome of a build or fetch in <dir>/.llamira.json.
func finishBuild(b *Build, dir string, err error, logf func(string, ...any)) {
	b.BuiltAt = time.Now()
	b.OK = err == nil
	if err != nil {
		b.Error = err.Error()
		logf("FAILED: %v", err)
	} else {
		logf("build %s ready: %s", b.Ref, b.Version)
	}
	if data, e := json.MarshalIndent(b, "", "  "); e == nil && dirExists(dir) {
		_ = os.WriteFile(filepath.Join(dir, buildMeta), data, 0o644)
	}
}
