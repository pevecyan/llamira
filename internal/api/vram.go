package api

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"llamira/internal/gpu"
	"llamira/internal/models"
	"llamira/internal/presets"
	"llamira/internal/vram"
)

type vramRequest struct {
	Text    string       `json:"text"`
	Section string       `json:"section"`
	Entries []presets.KV `json:"entries"`
}

// vramInputs resolves the model, effective options ([*] merged with the section, or with
// entries when given: the form's unsaved state) and GPUs for an estimate. code is the HTTP
// status to use on error.
func (s *Server) vramInputs(ctx context.Context, req vramRequest) (model vram.Model, label string, opts vram.Options, gpus []vram.GPU, code int, err error) {
	cat, err := s.Catalog.Get(ctx)
	if err != nil {
		return model, "", nil, nil, 500, err
	}
	doc := presets.Parse(req.Text)
	opts = vram.Options{}
	merge := func(kvs []presets.KV) {
		for _, kv := range kvs {
			key, val := kv.Key, strings.TrimSpace(kv.Value)
			if a, ok := cat.Lookup(key); ok {
				if a.IsFlag() && contains(a.NegNames, key) { // no-xyz = true → xyz = false
					val = invertBool(val)
				}
				key = a.Key
			}
			opts[key] = val
		}
	}
	if g := doc.Section(presets.GlobalSection); g != nil {
		merge(g.Entries())
	}
	if req.Entries != nil {
		merge(req.Entries)
	} else if sec := doc.Section(req.Section); sec != nil {
		merge(sec.Entries())
	}
	if model, label, err = s.resolveModel(ctx, req.Section, opts); err != nil {
		return model, "", nil, nil, 422, err
	}
	if gpus, err = s.gpusForEstimate(ctx); err != nil {
		return model, "", nil, nil, 500, fmt.Errorf("GPU info: %w", err)
	}
	return model, label, opts, gpus, 0, nil
}

// vramEstimate estimates VRAM for a preset section of a working-copy text.
func (s *Server) vramEstimate(w http.ResponseWriter, r *http.Request) {
	var req vramRequest
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "%v", err)
		return
	}
	model, label, opts, gpus, code, err := s.vramInputs(r.Context(), req)
	if err != nil {
		writeErr(w, code, "%v", err)
		return
	}
	est, err := vram.Run(model, opts, gpus)
	if err != nil {
		writeErr(w, 422, "%v", err)
		return
	}
	resp := map[string]any{"model": label, "estimate": est}
	// the CPU part (token embeddings, offloaded layers, cpu-moe experts) lives in host RAM
	if h := s.Host.Latest(); h != nil && h.MemTotal > 0 {
		avail := h.AvailableForUnit()
		resp["ram"] = map[string]uint64{"available": avail, "total": h.MemTotal}
		if est.CPU.Total > 0 && uint64(est.CPU.Total) > avail {
			est.Warnings = append(est.Warnings, fmt.Sprintf(
				"the CPU part needs %.1f GiB of RAM but only about %.1f GiB is available to llama.cpp: the host will swap (very slow)",
				float64(est.CPU.Total)/(1<<30), float64(avail)/(1<<30)))
		}
	}
	writeJSON(w, 200, resp)
}

// vramFit finds the largest context that is estimated to fit with the section's other settings.
func (s *Server) vramFit(w http.ResponseWriter, r *http.Request) {
	var req vramRequest
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "%v", err)
		return
	}
	model, label, opts, gpus, code, err := s.vramInputs(r.Context(), req)
	if err != nil {
		writeErr(w, code, "%v", err)
		return
	}
	if len(gpus) == 0 {
		writeErr(w, 422, "no GPU found")
		return
	}
	fit, err := vram.Fit(model, opts, gpus)
	if err != nil {
		writeErr(w, 422, "%v", err)
		return
	}
	writeJSON(w, 200, map[string]any{"model": label, "fit": fit})
}

// resolveModel finds the GGUF file(s) for a section: an explicit model path, an hf-repo
// reference, or the section name itself when it overrides a cached model.
func (s *Server) resolveModel(ctx context.Context, section string, opts vram.Options) (vram.Model, string, error) {
	if p := opts["model"]; p != "" {
		if msg := s.Apply.CheckModelPath(p); msg != "" {
			return vram.Model{}, "", fmt.Errorf("%s", msg)
		}
		m := vram.Model{Files: []string{p}, MMProj: opts["mmproj"]}
		if m.MMProj == "" { // models-dir layout: mmproj*.gguf next to the model
			if matches, _ := filepath.Glob(filepath.Join(filepath.Dir(p), "mmproj*.gguf")); len(matches) > 0 {
				m.MMProj = matches[0]
			}
		}
		if m.MMProj != "" {
			if _, err := os.Stat(m.MMProj); err != nil {
				m.MMProj = ""
			}
		}
		return m, p, nil
	}
	id := opts["hf-repo"]
	if id == "" {
		id = section
	}
	if id == "" || id == presets.GlobalSection {
		return vram.Model{}, "", fmt.Errorf("no model selected")
	}
	list, err := models.ScanCache(s.cache(ctx))
	if err != nil {
		return vram.Model{}, "", err
	}
	for _, lm := range list {
		if strings.EqualFold(lm.ID, id) {
			if lm.Error != "" {
				return vram.Model{}, "", fmt.Errorf("%s: %s", id, lm.Error)
			}
			return vram.Model{Files: lm.Paths, MMProj: lm.MMProjPath}, lm.ID, nil
		}
	}
	return vram.Model{}, "", fmt.Errorf("%s is not downloaded", id)
}

func (s *Server) gpusForEstimate(ctx context.Context) ([]vram.GPU, error) {
	gs, err := gpu.Read(ctx)
	if err != nil {
		return nil, err
	}
	apps, _ := gpu.Apps(ctx) // NVIDIA only
	reserved := gpu.ReservedMiB(gs, apps, "llama-server")
	// amdgpu has no per-process memory in sysfs: while a model is running its memory can't be
	// told apart from other users, so nothing counts as reserved then
	if slices.ContainsFunc(gs, func(g gpu.GPU) bool { return g.Vendor == "amd" }) && s.anyModelRunning(ctx) {
		for _, g := range gs {
			if g.Vendor == "amd" {
				reserved[g.Index] = 0
			}
		}
	}
	names := s.deviceNames(ctx, gs, true)
	out := make([]vram.GPU, len(gs))
	for i, g := range gs {
		out[i] = vram.GPU{
			Name:     names[i],
			Total:    int64(g.MemTotalMiB) * vram.MiB,
			Reserved: int64(reserved[g.Index]) * vram.MiB,
		}
	}
	return out, nil
}

func (s *Server) anyModelRunning(ctx context.Context) bool {
	list, err := s.Router.Models(ctx, false)
	if err != nil {
		return false
	}
	for _, m := range list {
		if v := m.Status.Value; v == "loaded" || v == "loading" || v == "sleeping" {
			return true
		}
	}
	return false
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func invertBool(v string) string {
	switch strings.ToLower(v) {
	case "false", "0", "off":
		return "true"
	}
	return "false"
}
