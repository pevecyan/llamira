package api

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"llamira/internal/llamacpp"
	"llamira/internal/models"
	"llamira/internal/presets"
	"llamira/internal/system"
)

// cache returns the router's download cache directory (LLAMA_CACHE).
func (s *Server) cache(ctx context.Context) string {
	s.cacheOnce.Do(func() {
		s.cacheDir = s.Cfg.Router.CacheDir
		if s.cacheDir != "" {
			return
		}
		env := s.Cfg.Router.Env
		if len(env) == 0 {
			env, _ = system.UnitEnv(ctx, s.Cfg.Router.Service)
		}
		for _, e := range env {
			if v, ok := strings.CutPrefix(e, "LLAMA_CACHE="); ok {
				s.cacheDir = v
			}
		}
		if s.cacheDir == "" {
			home, _ := os.UserHomeDir()
			s.cacheDir = filepath.Join(home, ".cache", "llama.cpp")
		}
	})
	return s.cacheDir
}

func (s *Server) hfSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	res, err := s.HF.Search(r.Context(), q, limit)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "%v", err)
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) hfRepo(w http.ResponseWriter, r *http.Request) {
	d, err := s.HF.Repo(r.Context(), r.URL.Query().Get("id"))
	if err != nil {
		writeErr(w, http.StatusBadGateway, "%v", err)
		return
	}
	writeJSON(w, 200, d)
}

func (s *Server) local(w http.ResponseWriter, r *http.Request) {
	dir := s.cache(r.Context())
	list, err := models.ScanCache(dir)
	resp := map[string]any{"cache_dir": dir, "models": list}
	if err != nil {
		resp["error"] = err.Error()
	}
	if list == nil {
		resp["models"] = []models.LocalModel{}
	}
	// GGUF files in the models directory that are not part of the download cache
	resp["dir_files"] = models.ScanDir(s.Cfg.Router.ModelsDir, dir)
	// preset sections that use each model (by section name or hf-repo)
	refs := map[string][]string{}
	if text, _, err := s.Apply.Read(); err == nil {
		if cat, err := s.Catalog.Get(r.Context()); err == nil {
			for _, sec := range presets.Parse(text).Sections {
				if sec.Name == presets.GlobalSection {
					continue
				}
				refs[sec.Name] = append(refs[sec.Name], sec.Name)
				for _, kv := range sec.Entries() {
					if c := cat.Canonical(kv.Key); (c == "hf-repo" || c == "model") && kv.Value != sec.Name {
						refs[kv.Value] = append(refs[kv.Value], sec.Name)
					}
				}
			}
		}
	}
	resp["preset_refs"] = refs
	resp["auto_presets"] = s.pendingAutoPresets()
	resp["disk_low_gb"] = s.alertSettings().DiskFreeGB // the storage bar warns at the alert threshold
	if d, err := models.DiskUsage(s.Cfg.Router.ModelsDir); err == nil {
		resp["disk"] = d
	}
	writeJSON(w, 200, resp)
}

var (
	reModelRef = regexp.MustCompile(`^[A-Za-z0-9][\w.-]*/[\w.-]+:[A-Za-z0-9_]+$`)
	reDevice   = regexp.MustCompile(`^([A-Za-z]+[0-9]+(,[A-Za-z]+[0-9]+)*)?$`)
)

func (s *Server) download(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model string `json:"model"`
		// optional: add a preset section for the model once the download finishes
		Preset *struct {
			Ctx        int    `json:"ctx"`
			Device     string `json:"device"`
			Embeddings bool   `json:"embeddings"`
		} `json:"preset"`
	}
	if err := readJSON(r, &req); err != nil || !reModelRef.MatchString(req.Model) {
		writeErr(w, 400, "model must look like org/repo:QUANT")
		return
	}
	if p := req.Preset; p != nil && (p.Ctx < 0 || p.Ctx > 1<<24 || !reDevice.MatchString(p.Device)) {
		writeErr(w, 400, "invalid preset settings")
		return
	}
	// The router validates against the Hub before it starts; that can take a few seconds.
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	if err := s.Router.Download(ctx, req.Model); err != nil {
		routerErr(w, err)
		return
	}
	s.Log.Info("download started", "model", req.Model, "preset", req.Preset != nil)
	if p := req.Preset; p != nil {
		s.queueAutoPreset(presetIntent{Model: req.Model, Ctx: p.Ctx, Device: p.Device, Embed: p.Embeddings})
	}
	writeJSON(w, 200, map[string]bool{"success": true})
}

// deleteModel removes a cached model through the router, but only when it is idle and no
// preset uses it. (The router itself would stop a running model and delete it anyway.)
func (s *Server) deleteModel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model string `json:"model"`
	}
	if err := readJSON(r, &req); err != nil || req.Model == "" {
		writeErr(w, 400, "body must be {\"model\": \"<id>\"}")
		return
	}
	list, err := s.Router.Models(r.Context(), false)
	if err != nil {
		routerErr(w, err)
		return
	}
	var m *llamacpp.Model
	for i := range list {
		if list[i].ID == req.Model {
			m = &list[i]
		}
	}
	switch {
	case m == nil:
		writeErr(w, 404, "model %s not found", req.Model)
		return
	case m.Source != "cache":
		writeErr(w, 409, "only downloaded (cache) models can be deleted; %s comes from %s", m.ID, m.Source)
		return
	case m.Status.Value == "downloading":
		writeErr(w, 409, "%s is downloading: cancel the download instead", m.ID)
		return
	case m.Status.Value != "unloaded":
		writeErr(w, 409, "%s is %s: unload it first", m.ID, m.Status.Value)
		return
	}
	if refs, err := s.presetRefs(r.Context(), m.ID); err != nil {
		writeErr(w, 500, "%v", err)
		return
	} else if len(refs) > 0 {
		writeErr(w, 409, "%s is used by preset section(s) %s: remove them first", m.ID, strings.Join(refs, ", "))
		return
	}
	if err := s.Router.Delete(r.Context(), m.ID); err != nil {
		routerErr(w, err)
		return
	}
	s.Log.Info("model deleted", "model", m.ID)
	writeJSON(w, 200, map[string]bool{"success": true})
}

// presetRefs lists preset sections that override or reference a model id.
func (s *Server) presetRefs(ctx context.Context, id string) ([]string, error) {
	text, _, err := s.Apply.Read()
	if err != nil {
		return nil, err
	}
	cat, err := s.Catalog.Get(ctx)
	if err != nil {
		return nil, err
	}
	var refs []string
	for _, sec := range presets.Parse(text).Sections {
		if sec.Name == id {
			refs = append(refs, fmt.Sprintf("[%s]", sec.Name))
			continue
		}
		for _, kv := range sec.Entries() {
			if cat.Canonical(kv.Key) == "hf-repo" && strings.EqualFold(kv.Value, id) {
				refs = append(refs, fmt.Sprintf("[%s]", sec.Name))
				break
			}
		}
	}
	return refs, nil
}

// recommendations evaluates the curated catalogue against this host's GPUs.
func (s *Server) recommendations(w http.ResponseWriter, r *http.Request) {
	gpus, err := s.gpusForEstimate(r.Context())
	if err != nil {
		writeErr(w, 500, "GPU info: %v", err)
		return
	}
	var installed []string
	if list, err := models.ScanCache(s.cache(r.Context())); err == nil {
		for _, m := range list {
			installed = append(installed, m.ID)
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	res, cat := s.Recommend.Recommend(ctx, gpus, installed, s.Arch.For(s.ActiveBinary(ctx)), r.URL.Query().Get("refresh") != "")
	writeJSON(w, 200, map[string]any{"catalog": map[string]string{"version": cat.Version, "source": cat.Source}, "gpus": gpus, "results": res})
}
