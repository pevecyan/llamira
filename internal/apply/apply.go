// Package apply validates and safely applies changes to the router preset file:
// static checks → dry-run on a throwaway router → backup → atomic write → hot reload →
// verify → rollback on failure.
package apply

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"llamira/internal/llamacpp"
	"llamira/internal/presets"
	"llamira/internal/store"
	"llamira/internal/system"
)

type Manager struct {
	PresetPath     string
	BackupDir      string
	Keep           int
	ModelsDir      string
	Service        string
	Router         *llamacpp.Client
	Catalog        *llamacpp.CatalogCache
	DryRunOpts     llamacpp.DryRunOptions
	EnvFromSystemd bool
	BinaryFunc     func(context.Context) string // current llama-server binary (follows updates)
	Store          *store.Store

	mu      sync.Mutex
	envOnce sync.Once
	env     []string
}

// ErrConflict is returned when the preset file changed since the caller read it.
var ErrConflict = errors.New("preset file changed since it was loaded; reload and try again")

func Hash(text string) string {
	h := sha256.Sum256([]byte(text))
	return hex.EncodeToString(h[:8])
}

// Read returns the current preset text and its hash. A missing file reads as empty.
func (m *Manager) Read() (string, string, error) {
	b, err := os.ReadFile(m.PresetPath)
	if errors.Is(err, os.ErrNotExist) {
		return "", Hash(""), nil
	}
	if err != nil {
		return "", "", err
	}
	return string(b), Hash(string(b)), nil
}

func (m *Manager) dryRunOptions(ctx context.Context) llamacpp.DryRunOptions {
	m.envOnce.Do(func() {
		m.env = m.DryRunOpts.Env
		if len(m.env) == 0 && m.EnvFromSystemd {
			m.env, _ = system.UnitEnv(ctx, m.Service)
		}
	})
	o := m.DryRunOpts
	o.Env = m.env
	if m.BinaryFunc != nil {
		if b := m.BinaryFunc(ctx); b != "" {
			o.Binary = b
		}
	}
	return o
}

// Issue is a validation finding.
type Issue struct {
	Severity string `json:"severity"` // error, warning
	Section  string `json:"section,omitempty"`
	Key      string `json:"key,omitempty"`
	Message  string `json:"message"`
}

func hasErrors(issues []Issue) bool {
	return slices.ContainsFunc(issues, func(i Issue) bool { return i.Severity == "error" })
}

var modelSourceKeys = []string{"model", "hf-repo", "model-url", "docker-repo"}

// Validate runs static checks. liveIDs are the model ids currently known to the router
// (a section named after an existing id may omit a model source).
func (m *Manager) Validate(ctx context.Context, text string, liveIDs []string) ([]Issue, error) {
	cat, err := m.Catalog.Get(ctx)
	if err != nil {
		return nil, err
	}
	d := presets.Parse(text)
	var issues []Issue
	add := func(sev, sec, key, format string, a ...any) {
		issues = append(issues, Issue{sev, sec, key, fmt.Sprintf(format, a...)})
	}
	for _, kv := range d.Preamble.Entries() {
		if kv.Key != "version" {
			add("warning", "", kv.Key, "top-level key %q outside any section", kv.Key)
		}
	}
	seen := map[string]bool{}
	// aliases must be unique and must not shadow a model id (the router skips conflicts)
	aliasOwner := map[string]string{}
	ids := map[string]bool{}
	for _, id := range liveIDs {
		ids[id] = true
	}
	for _, s := range d.Sections {
		ids[s.Name] = true
	}
	for _, s := range d.Sections {
		for _, kv := range s.Entries() {
			if cat.Canonical(kv.Key) != "alias" {
				continue
			}
			for _, a := range strings.Split(kv.Value, ",") {
				a = strings.TrimSpace(a)
				switch {
				case a == "":
				case ids[a] && a != s.Name:
					add("error", s.Name, kv.Key, "alias %q is already a model id", a)
				case aliasOwner[a] != "" && aliasOwner[a] != s.Name:
					add("error", s.Name, kv.Key, "alias %q is also defined in [%s]", a, aliasOwner[a])
				default:
					aliasOwner[a] = s.Name
				}
			}
		}
	}
	for _, s := range d.Sections {
		if s.Name == "" {
			add("error", s.Name, "", "empty section name")
			continue
		}
		if seen[s.Name] {
			add("error", s.Name, "", "duplicate section [%s]", s.Name)
		}
		seen[s.Name] = true

		keys := map[string]string{}
		for _, kv := range s.Entries() {
			if !cat.Known(kv.Key) {
				add("error", s.Name, kv.Key, "unknown option %q (not supported by llama-server %s)", kv.Key, cat.Version)
				continue
			}
			canon := cat.Canonical(kv.Key)
			if prev, dup := keys[canon]; dup {
				add("warning", s.Name, kv.Key, "%q duplicates %q (same option)", kv.Key, prev)
			}
			keys[canon] = kv.Key
			if llamacpp.RouterControlledKeys[canon] {
				add("warning", s.Name, kv.Key, "%q is controlled by the router and will be ignored", kv.Key)
			}
			if a, ok := cat.Lookup(kv.Key); ok {
				if len(a.Allowed) > 0 && !slices.Contains(a.Allowed, kv.Value) {
					add("error", s.Name, kv.Key, "%s: %q is not one of %s", kv.Key, kv.Value, strings.Join(a.Allowed, ", "))
				}
				if a.IsFlag() && !isBool(kv.Value) {
					add("warning", s.Name, kv.Key, "%s is an on/off flag; expected true or false, got %q", kv.Key, kv.Value)
				}
			}
			if canon == "model" {
				if msg := m.checkModelPath(kv.Value); msg != "" {
					add("error", s.Name, kv.Key, "%s", msg)
				}
			}
		}
		hasSource := false
		for _, k := range modelSourceKeys {
			if _, ok := keys[k]; ok {
				hasSource = true
			}
		}
		if s.Name == presets.GlobalSection {
			if hasSource {
				add("warning", s.Name, "", "a model source in [*] applies to every preset")
			}
		} else if !hasSource && !slices.Contains(liveIDs, s.Name) {
			add("error", s.Name, "", "[%s] is not an existing model id, so it needs hf-repo or model", s.Name)
		}
	}
	return issues, nil
}

func isBool(v string) bool {
	switch strings.ToLower(v) {
	case "true", "false", "1", "0", "on", "off":
		return true
	}
	return false
}

// checkModelPath makes sure a model path is absolute, exists and resolves inside ModelsDir.
func (m *Manager) checkModelPath(p string) string {
	if !filepath.IsAbs(p) {
		return fmt.Sprintf("model path %q must be absolute", p)
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return fmt.Sprintf("model file %q not found", p)
	}
	if m.ModelsDir != "" {
		root, err := filepath.EvalSymlinks(m.ModelsDir)
		if err != nil {
			root = m.ModelsDir
		}
		if rel, err := filepath.Rel(root, real); err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
			return fmt.Sprintf("model file %q is outside %s", p, m.ModelsDir)
		}
	}
	return ""
}

// ModelChange describes how a model id is affected by a preset change.
type ModelChange struct {
	ID        string `json:"id"`
	Change    string `json:"change"` // added, removed, changed
	Status    string `json:"status"` // current live status
	Unloads   bool   `json:"unloads"`
	OldPreset string `json:"old_preset,omitempty"`
	NewPreset string `json:"new_preset,omitempty"`
}

// Preview is the result of checking a proposed preset text without applying it.
type Preview struct {
	Text      string             `json:"text"`
	BaseHash  string             `json:"base_hash"`
	Diff      []presets.DiffLine `json:"diff"`
	Changed   bool               `json:"changed"`
	Issues    []Issue            `json:"issues"`
	DryRunOK  bool               `json:"dry_run_ok"`
	DryRunErr string             `json:"dry_run_error,omitempty"`
	DryRunLog string             `json:"dry_run_log,omitempty"`
	Models    []ModelChange      `json:"models"`
	dryModels []llamacpp.Model
}

// OK reports whether the preview allows applying.
func (p *Preview) OK() bool { return p.DryRunOK && !hasErrors(p.Issues) }

// Preview validates text (static + dry-run) and computes the diff and model impact.
func (m *Manager) Preview(ctx context.Context, text string) (*Preview, error) {
	cur, hash, err := m.Read()
	if err != nil {
		return nil, err
	}
	live, err := m.Router.Models(ctx, false)
	if err != nil {
		return nil, fmt.Errorf("router: %w", err)
	}
	var liveIDs []string
	for _, lm := range live {
		liveIDs = append(liveIDs, lm.ID)
	}
	p := &Preview{Text: text, BaseHash: hash, Diff: presets.Diff(cur, text)}
	p.Changed = presets.Changed(p.Diff)
	if p.Issues, err = m.Validate(ctx, text, liveIDs); err != nil {
		return nil, err
	}
	if p.Issues == nil {
		p.Issues = []Issue{}
	}
	if hasErrors(p.Issues) {
		p.DryRunErr = "skipped: fix validation errors first"
		return p, nil
	}
	res, err := llamacpp.DryRun(ctx, m.dryRunOptions(ctx), text)
	if res != nil {
		p.DryRunLog = res.Log
	}
	if err != nil {
		p.DryRunErr = err.Error()
		return p, nil
	}
	p.DryRunOK = true
	p.dryModels = res.Models
	p.DryRunLog = ""
	// Every section must show up as a model.
	dryByID := map[string]llamacpp.Model{}
	for _, dm := range res.Models {
		dryByID[dm.ID] = dm
	}
	for _, name := range presets.Parse(text).Names() {
		if name == presets.GlobalSection {
			continue
		}
		if _, ok := dryByID[name]; !ok {
			p.Issues = append(p.Issues, Issue{"error", name, "", fmt.Sprintf("[%s] did not appear in the router's model list", name)})
		}
	}
	p.Models = impact(live, res.Models)
	return p, nil
}

// impact compares live models with the dry-run result.
func impact(live, dry []llamacpp.Model) []ModelChange {
	dryByID := map[string]llamacpp.Model{}
	for _, d := range dry {
		dryByID[d.ID] = d
	}
	var out []ModelChange
	liveIDs := map[string]bool{}
	for _, l := range live {
		liveIDs[l.ID] = true
		running := l.Status.Value == "loaded" || l.Status.Value == "loading" || l.Status.Value == "sleeping"
		d, ok := dryByID[l.ID]
		switch {
		case !ok:
			out = append(out, ModelChange{ID: l.ID, Change: "removed", Status: l.Status.Value, Unloads: running, OldPreset: l.Status.Preset})
		case d.Status.Preset != l.Status.Preset:
			out = append(out, ModelChange{ID: l.ID, Change: "changed", Status: l.Status.Value, Unloads: running, OldPreset: l.Status.Preset, NewPreset: d.Status.Preset})
		case !sameSet(d.Aliases, l.Aliases):
			// the router only re-reads aliases of models that are not running: the manager
			// reloads a running model briefly to activate them (see syncAliases)
			out = append(out, ModelChange{ID: l.ID, Change: "aliases", Status: l.Status.Value, Unloads: running,
				OldPreset: strings.Join(l.Aliases, ","), NewPreset: strings.Join(d.Aliases, ",")})
		}
	}
	for _, d := range dry {
		if !liveIDs[d.ID] {
			out = append(out, ModelChange{ID: d.ID, Change: "added", Status: "unloaded", NewPreset: d.Status.Preset})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]bool{}
	for _, x := range a {
		m[x] = true
	}
	for _, x := range b {
		if !m[x] {
			return false
		}
	}
	return true
}

func isRunning(m llamacpp.Model) bool {
	switch m.Status.Value {
	case "loaded", "loading", "sleeping":
		return true
	}
	return false
}

// syncAliases makes the router's aliases match the dry-run result. Aliases of running models
// are not updated by a hot reload, so those models are unloaded, the list reloaded and the
// models loaded again.
func (m *Manager) syncAliases(ctx context.Context, want []llamacpp.Model) (string, error) {
	wantBy := map[string][]string{}
	for _, w := range want {
		wantBy[w.ID] = w.Aliases
	}
	live, err := m.Router.Models(ctx, false)
	if err != nil {
		return "", err
	}
	var reload []string
	for _, l := range live {
		if w, ok := wantBy[l.ID]; ok && !sameSet(w, l.Aliases) && isRunning(l) {
			if err := m.Router.Unload(ctx, l.ID); err != nil {
				return "", fmt.Errorf("unload %s: %w", l.ID, err)
			}
			reload = append(reload, l.ID)
		}
	}
	if len(reload) == 0 {
		return "aliases already active", nil
	}
	deadline := time.Now().Add(90 * time.Second)
	for {
		live, err = m.Router.Models(ctx, false)
		if err != nil {
			return "", err
		}
		busy := false
		for _, l := range live {
			if slices.Contains(reload, l.ID) && isRunning(l) {
				busy = true
			}
		}
		if !busy {
			break
		}
		if time.Now().After(deadline) {
			return "", errors.New("timed out waiting for models to unload")
		}
		time.Sleep(500 * time.Millisecond)
	}
	if live, err = m.Router.Models(ctx, true); err != nil {
		return "", err
	}
	for _, l := range live {
		if w, ok := wantBy[l.ID]; ok && !sameSet(w, l.Aliases) {
			return "", fmt.Errorf("aliases of %s did not activate (have %v, want %v)", l.ID, l.Aliases, w)
		}
	}
	for _, id := range reload {
		_ = m.Router.Load(ctx, id) // reload in the background; requests wait for it
	}
	return fmt.Sprintf("activated aliases; reloading %s", strings.Join(reload, ", ")), nil
}

// Step is one stage of an apply.
type Step struct {
	Name     string `json:"name"`
	OK       bool   `json:"ok"`
	Detail   string `json:"detail,omitempty"`
	Duration int64  `json:"duration_ms"`
}

// Result is the outcome of an apply.
type Result struct {
	OK         bool          `json:"ok"`
	Error      string        `json:"error,omitempty"`
	Steps      []Step        `json:"steps"`
	Backup     string        `json:"backup,omitempty"`
	RolledBack bool          `json:"rolled_back"`
	Models     []ModelChange `json:"models"`
	ApplyID    int64         `json:"apply_id"`
}

// Apply runs the full pipeline. baseHash (if set) must match the current file.
func (m *Manager) Apply(ctx context.Context, text, baseHash, summary string) (*Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	before, hash, err := m.Read()
	if err != nil {
		return nil, err
	}
	if baseHash != "" && baseHash != hash {
		return nil, ErrConflict
	}
	r := &Result{Steps: []Step{}}
	step := func(name string, fn func() (string, error)) bool {
		t := time.Now()
		detail, err := fn()
		s := Step{Name: name, OK: err == nil, Detail: detail, Duration: time.Since(t).Milliseconds()}
		if err != nil {
			s.Detail = err.Error()
			r.Error = fmt.Sprintf("%s: %v", name, err)
		}
		r.Steps = append(r.Steps, s)
		return err == nil
	}
	defer func() { m.record(r, summary, before, text) }()

	var p *Preview
	if !step("validate", func() (string, error) {
		p, err = m.Preview(ctx, text)
		if err != nil {
			return "", err
		}
		if hasErrors(p.Issues) {
			var msgs []string
			for _, i := range p.Issues {
				if i.Severity == "error" {
					msgs = append(msgs, fmt.Sprintf("[%s] %s", i.Section, i.Message))
				}
			}
			return "", errors.New(strings.Join(msgs, "; "))
		}
		return fmt.Sprintf("%d warning(s)", len(p.Issues)), nil
	}) {
		return r, nil
	}
	r.Models = p.Models
	if !step("dry-run", func() (string, error) {
		if !p.DryRunOK {
			return "", errors.New(p.DryRunErr)
		}
		return fmt.Sprintf("throwaway router listed %d models", len(p.dryModels)), nil
	}) {
		return r, nil
	}
	if !p.Changed {
		r.OK = true
		r.Steps = append(r.Steps, Step{Name: "write", OK: true, Detail: "no changes"})
		return r, nil
	}
	if !step("backup", func() (string, error) {
		if before == "" {
			return "no previous file", nil
		}
		r.Backup, err = m.backup(before)
		return r.Backup, err
	}) {
		return r, nil
	}
	if !step("write", func() (string, error) { return m.PresetPath, WriteAtomic(m.PresetPath, text) }) {
		return r, nil
	}
	ok := step("reload", func() (string, error) {
		_, err := m.Router.Models(ctx, true)
		return "GET /models?reload=1", err
	}) && step("verify", func() (string, error) { return m.verify(ctx, p.dryModels) }) &&
		step("aliases", func() (string, error) { return m.syncAliases(ctx, p.dryModels) })
	if ok {
		r.OK = true
		return r, nil
	}
	r.RolledBack = true
	step("rollback", func() (string, error) { return m.rollback(ctx, before) })
	return r, nil
}

// verify checks that the live router now matches what the dry-run reported.
func (m *Manager) verify(ctx context.Context, want []llamacpp.Model) (string, error) {
	if err := m.Router.Health(ctx); err != nil {
		return "", fmt.Errorf("health: %w", err)
	}
	live, err := m.Router.Models(ctx, false)
	if err != nil {
		return "", err
	}
	liveByID := map[string]llamacpp.Model{}
	for _, l := range live {
		liveByID[l.ID] = l
	}
	for _, w := range want {
		l, ok := liveByID[w.ID]
		if !ok {
			return "", fmt.Errorf("model %s missing after reload", w.ID)
		}
		if l.Status.Preset != w.Status.Preset {
			return "", fmt.Errorf("model %s: effective preset differs from dry-run", w.ID)
		}
	}
	return fmt.Sprintf("%d models match", len(want)), nil
}

func (m *Manager) rollback(ctx context.Context, before string) (string, error) {
	if err := WriteAtomic(m.PresetPath, before); err != nil {
		return "", fmt.Errorf("restore file: %w", err)
	}
	if _, err := m.Router.Models(ctx, true); err == nil && m.Router.Health(ctx) == nil {
		return "previous file restored and reloaded", nil
	}
	if err := system.Restart(ctx, m.Service); err != nil {
		return "", fmt.Errorf("reload failed and restart failed: %w", err)
	}
	return "previous file restored; router restarted", nil
}

func (m *Manager) backup(text string) (string, error) {
	if err := os.MkdirAll(m.BackupDir, 0o755); err != nil {
		return "", err
	}
	name := filepath.Join(m.BackupDir, "models.ini."+time.Now().UTC().Format("20060102T150405.000Z"))
	if err := os.WriteFile(name, []byte(text), 0o644); err != nil {
		return "", err
	}
	m.pruneBackups()
	return name, nil
}

func (m *Manager) pruneBackups() {
	matches, _ := filepath.Glob(filepath.Join(m.BackupDir, "models.ini.*"))
	sort.Strings(matches)
	for len(matches) > max(m.Keep, 1) {
		os.Remove(matches[0])
		matches = matches[1:]
	}
}

// Backups lists backup files, newest first.
func (m *Manager) Backups() []string {
	matches, _ := filepath.Glob(filepath.Join(m.BackupDir, "models.ini.*"))
	sort.Sort(sort.Reverse(sort.StringSlice(matches)))
	return matches
}

func (m *Manager) record(r *Result, summary, before, after string) {
	if m.Store == nil {
		return
	}
	steps, _ := json.Marshal(r.Steps)
	if summary == "" {
		summary = "apply"
	}
	id, err := m.Store.AddApply(store.Apply{
		Time: time.Now(), OK: r.OK, Summary: summary, Error: r.Error,
		Steps: steps, Backup: r.Backup, Before: before, After: after,
	})
	if err == nil {
		r.ApplyID = id
	}
}

// WriteAtomic writes a file via temp file + fsync + rename in the same directory.
func WriteAtomic(path, text string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".models.ini.tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.WriteString(text); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
	return nil
}

// CheckModelPath reports why a model path is not acceptable ("" if it is).
func (m *Manager) CheckModelPath(p string) string { return m.checkModelPath(p) }
