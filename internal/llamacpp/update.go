package llamacpp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultBuildsDir holds one directory per built llama.cpp version.
const DefaultBuildsDir = "/opt/llama.cpp-builds"

// buildMeta is written to <build>/.llamira.json when a build finishes.
const buildMeta = ".llamira.json"

var reRef = regexp.MustCompile(`^(b[0-9]{3,6}|[0-9a-f]{7,40}|master)$`)

// ValidRef reports whether ref is an accepted llama.cpp version reference:
// a release tag (b11050), a commit hash, or "master".
func ValidRef(ref string) bool { return reRef.MatchString(ref) }

// Build describes one built version.
type Build struct {
	Ref      string    `json:"ref"`
	Dir      string    `json:"dir"`
	Binary   string    `json:"binary"`
	Commit   string    `json:"commit,omitempty"`
	Version  string    `json:"version,omitempty"`
	BuiltAt  time.Time `json:"built_at"`
	OK       bool      `json:"ok"`
	Error    string    `json:"error,omitempty"`
	DryRun   string    `json:"dry_run,omitempty"` // validation summary
	Original bool      `json:"original,omitempty"`
	Source   string    `json:"source,omitempty"` // "source" or "prebuilt <flavor>"
	Active   bool      `json:"active,omitempty"`
	Running  bool      `json:"running,omitempty"` // build in progress
}

// BuildUnit is the transient systemd unit name of a build job.
func BuildUnit(ref string) string { return "llamira-build-" + ref + ".service" }

// ListBuilds returns the original build plus all builds in buildsDir.
func ListBuilds(srcDir, buildsDir string) []Build {
	var out []Build
	orig := Build{Ref: "original", Dir: srcDir, Binary: filepath.Join(srcDir, "build", "bin", "llama-server"), OK: true, Original: true}
	if st, err := os.Stat(orig.Binary); err == nil { // fresh installs have no original build
		orig.BuiltAt = st.ModTime()
		out = append(out, orig)
	}
	entries, _ := os.ReadDir(buildsDir)
	for _, e := range entries {
		if !e.IsDir() || !ValidRef(e.Name()) {
			continue
		}
		dir := filepath.Join(buildsDir, e.Name())
		b := Build{Ref: e.Name(), Dir: dir, Binary: filepath.Join(dir, "build", "bin", "llama-server")}
		data, err := os.ReadFile(filepath.Join(dir, buildMeta))
		if err == nil {
			_ = json.Unmarshal(data, &b)
			b.Dir, b.Binary = dir, filepath.Join(dir, "build", "bin", "llama-server")
		} else {
			b.Error = "incomplete"
		}
		out = append(out, b)
	}
	first := 0
	if len(out) > 0 && out[0].Original {
		first = 1
	}
	rest := out[first:]
	sort.SliceStable(rest, func(i, j int) bool { return rest[i].BuiltAt.After(rest[j].BuiltAt) })
	return out
}

// BinaryFor returns the llama-server path for a ref ("original" = the source tree's build).
func BinaryFor(srcDir, buildsDir, ref string) (string, error) {
	if ref == "original" {
		return filepath.Join(srcDir, "build", "bin", "llama-server"), nil
	}
	if !ValidRef(ref) {
		return "", fmt.Errorf("invalid ref %q", ref)
	}
	for _, b := range ListBuilds(srcDir, buildsDir) {
		if b.Ref == ref {
			if !b.OK {
				return "", fmt.Errorf("build %s is not usable: %s", ref, b.Error)
			}
			return b.Binary, nil
		}
	}
	return "", fmt.Errorf("build %s not found", ref)
}

// ---- build (runs as root inside a transient systemd unit) ---------------------------------

// BuildOptions configures RunBuild.
type BuildOptions struct {
	SrcDir     string   // existing llama.cpp git checkout (its build/CMakeCache.txt provides the flags)
	BuildsDir  string   // where /<ref> directories are created
	Ref        string   // version to build
	Env        []string // router environment (LLAMA_CACHE, LD_LIBRARY_PATH, ...) for validation
	PresetFile string   // validate the new binary against this preset file
	ModelsDir  string
	Flags      []string // cmake -D flags; empty = copy them from <SrcDir>/build/CMakeCache.txt
	Out        io.Writer
}

// cmake cache entries copied from the existing build
var copyFlags = regexp.MustCompile(`^(GGML_[A-Z0-9_]+|CMAKE_BUILD_TYPE|CMAKE_CUDA_ARCHITECTURES|CMAKE_CUDA_COMPILER|BUILD_SHARED_LIBS|LLAMA_CURL|LLAMA_OPENSSL|LLAMA_BUILD_MTMD|LLAMA_BUILD_UI):[A-Z]+=(.*)$`)

// CMakeFlags reads -D flags from an existing CMakeCache.txt.
func CMakeFlags(cachePath string) ([]string, error) {
	f, err := os.Open(cachePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var flags []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		m := copyFlags.FindStringSubmatch(sc.Text())
		if m == nil || strings.HasSuffix(m[1], "_DIR") {
			continue
		}
		flags = append(flags, "-D"+m[1]+"="+m[2])
	}
	flags = append(flags, "-DLLAMA_BUILD_TESTS=OFF", "-DLLAMA_BUILD_EXAMPLES=OFF")
	return flags, sc.Err()
}

// RunBuild checks out ref into its own worktree, builds llama-server with the existing build's
// flags and validates it. The outcome is recorded in <dir>/.llamira.json.
func RunBuild(ctx context.Context, o BuildOptions) (err error) {
	if !ValidRef(o.Ref) {
		return fmt.Errorf("invalid ref %q", o.Ref)
	}
	dir := filepath.Join(o.BuildsDir, o.Ref)
	b := Build{Ref: o.Ref, Dir: dir, Source: "source"}
	logf := func(format string, a ...any) { fmt.Fprintf(o.Out, "==> "+format+"\n", a...) }
	defer func() { finishBuild(&b, dir, err, logf) }()
	run := func(name string, args ...string) error {
		fmt.Fprintf(o.Out, "$ %s %s\n", name, strings.Join(args, " "))
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Stdout, cmd.Stderr = o.Out, o.Out
		cmd.Env = append(os.Environ(), "PATH=/usr/local/cuda/bin:"+os.Getenv("PATH"))
		return cmd.Run()
	}

	flags := o.Flags
	if len(flags) == 0 {
		if flags, err = CMakeFlags(filepath.Join(o.SrcDir, "build", "CMakeCache.txt")); err != nil {
			return fmt.Errorf("read existing build flags: %w", err)
		}
	}
	if err := os.MkdirAll(o.BuildsDir, 0o755); err != nil {
		return err
	}
	logf("fetching %s", o.Ref)
	if err := run("git", "-C", o.SrcDir, "fetch", "--tags", "origin"); err != nil {
		return fmt.Errorf("git fetch: %w", err)
	}
	target := o.Ref
	if o.Ref == "master" {
		target = "origin/master"
	}
	if dirExists(dir) {
		logf("removing previous attempt in %s", dir)
		_ = run("git", "-C", o.SrcDir, "worktree", "remove", "--force", dir)
		_ = os.RemoveAll(dir)
	}
	_ = run("git", "-C", o.SrcDir, "worktree", "prune")
	if err := run("git", "-C", o.SrcDir, "worktree", "add", "--detach", dir, target); err != nil {
		return fmt.Errorf("git worktree: %w", err)
	}
	if out, err := exec.Command("git", "-C", dir, "rev-parse", "--short=9", "HEAD").Output(); err == nil {
		b.Commit = strings.TrimSpace(string(out))
	}

	jobs := buildJobs()
	logf("configuring (%d flags from the existing build), then building llama-server with %d jobs", len(flags), jobs)
	args := append([]string{"-S", dir, "-B", filepath.Join(dir, "build")}, flags...)
	if err := run("cmake", args...); err != nil {
		return fmt.Errorf("cmake configure: %w", err)
	}
	if err := run("cmake", "--build", filepath.Join(dir, "build"), "--config", "Release", "-j", fmt.Sprint(jobs), "--target", "llama-server"); err != nil {
		return fmt.Errorf("cmake build: %w", err)
	}

	return validateBuild(ctx, &b, filepath.Join(dir, "build", "bin", "llama-server"), o.SrcDir, o.Env, o.PresetFile, o.ModelsDir, logf, o.Out)
}

// FreshFlags returns cmake flags for a build without an existing CMakeCache.txt.
func FreshFlags(h Host) []string {
	flags := []string{"-DCMAKE_BUILD_TYPE=Release", "-DBUILD_SHARED_LIBS=ON", "-DGGML_NATIVE=ON",
		"-DLLAMA_BUILD_TESTS=OFF", "-DLLAMA_BUILD_EXAMPLES=OFF", "-DLLAMA_OPENSSL=ON"}
	if len(h.GPUs) > 0 && h.NVCC != "" {
		flags = append(flags, "-DGGML_CUDA=ON")
		if a := h.CUDAArchitectures(); a != "" {
			flags = append(flags, "-DCMAKE_CUDA_ARCHITECTURES="+a)
		}
	}
	return flags
}

// buildJobs picks a parallelism that leaves CPU for inference and fits RAM (nvcc ~2.5 GB/job).
func buildJobs() int {
	jobs := max(runtime.NumCPU()-2, 1)
	if data, err := os.ReadFile("/proc/meminfo"); err == nil {
		var kb int
		for _, l := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(l, "MemAvailable:") {
				fmt.Sscanf(strings.TrimPrefix(l, "MemAvailable:"), "%d", &kb)
			}
		}
		if kb > 0 {
			jobs = min(jobs, max(kb/(2500*1024), 1))
		}
	}
	return jobs
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// RemoveBuild deletes a build directory and its git worktree registration.
func RemoveBuild(srcDir, buildsDir, ref string) error {
	if !ValidRef(ref) {
		return fmt.Errorf("invalid ref %q", ref)
	}
	dir := filepath.Join(buildsDir, ref)
	_ = exec.Command("git", "-C", srcDir, "worktree", "remove", "--force", dir).Run()
	_ = exec.Command("git", "-C", srcDir, "worktree", "prune").Run()
	return os.RemoveAll(dir)
}

// ---- available versions -------------------------------------------------------------------

// Release is an upstream llama.cpp release.
type Release struct {
	Tag       string    `json:"tag"`               // build tag (bNNNNN): what Download/Build use
	Version   string    `json:"version,omitempty"` // v0.6.0 for a version release (Tag is its build)
	Published time.Time `json:"published"`
	URL       string    `json:"url"`
	Notes     string    `json:"notes,omitempty"`
}

var reBuildNum = regexp.MustCompile(`^b?(\d{3,})`)

// BuildNumber extracts the build number from "b11036-911f6cdc8" (router build_info), "b11036"
// (a release tag) or "11036 (911f6cdc8)" (--version); 0 if there is none.
func BuildNumber(s string) int {
	m := reBuildNum.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

var (
	relMu    sync.Mutex
	relCache []Release
	relTime  time.Time
)

// releaseTitle returns the commit title of a release body (the first non-markup line).
func releaseTitle(body string) string {
	for _, l := range strings.Split(body, "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "<") || strings.HasPrefix(l, "#") {
			continue
		}
		if len(l) > 200 {
			l = l[:200] + "…"
		}
		return l
	}
	return ""
}

// Releases lists recent upstream releases (GitHub API, cached for 10 minutes).
func Releases(ctx context.Context) ([]Release, error) {
	relMu.Lock()
	defer relMu.Unlock()
	if relCache != nil && time.Since(relTime) < 10*time.Minute {
		return relCache, nil
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, GitHubAPI+"/repos/ggml-org/llama.cpp/releases?per_page=20", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("GitHub API: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var raw []struct {
		TagName     string    `json:"tag_name"`
		PublishedAt time.Time `json:"published_at"`
		HTMLURL     string    `json:"html_url"`
		Body        string    `json:"body"`
		Draft       bool      `json:"draft"`
		Prerelease  bool      `json:"prerelease"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	var out []Release
	for _, r := range raw {
		if r.Draft || !ValidRef(r.TagName) {
			continue
		}
		notes := releaseTitle(r.Body)
		out = append(out, Release{Tag: r.TagName, Published: r.PublishedAt, URL: r.HTMLURL, Notes: strings.TrimSpace(notes)})
	}
	if len(out) == 0 {
		return nil, errors.New("no releases found")
	}
	relCache, relTime = out, time.Now()
	return out, nil
}
