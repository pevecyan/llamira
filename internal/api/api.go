// Package api is the HTTP API served by `llamira serve` (JSON + Server-Sent Events).
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"llamira/internal/alerts"
	"llamira/internal/apply"
	"llamira/internal/bench"
	"llamira/internal/config"
	"llamira/internal/gpu"
	"llamira/internal/hf"
	"llamira/internal/hoststat"
	"llamira/internal/llamacpp"
	"llamira/internal/presets"
	"llamira/internal/recommend"
	"llamira/internal/schedule"
	"llamira/internal/store"
	"llamira/internal/system"
)

type Server struct {
	Cfg     config.Server
	Version string
	Router  *llamacpp.Client
	Catalog *llamacpp.CatalogCache
	Apply   *apply.Manager
	Store   *store.Store
	HF      *hf.Client
	Bench   *bench.Runner
	// ActiveBinary returns the llama-server binary the router currently runs.
	ActiveBinary func(context.Context) string
	ConfigPath   string
	Alerts       *alerts.Monitor
	Schedule     *schedule.Runner
	Recommend    *recommend.Engine
	Sweeper      *bench.Sweeper
	Host         *hoststat.Sampler // host RAM/CPU and the router unit's usage

	hostOnce sync.Once
	host     llamacpp.Host
	Arch     recommend.ArchSupport
	Log      *slog.Logger

	cacheOnce sync.Once
	cacheDir  string

	auto autoPresets

	updMu sync.Mutex
	upd   UpdateInfo

	devMu sync.Mutex
	dev   deviceCache
}

// Handler returns the API handler, mounted at /api/.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/info", s.info)
	mux.HandleFunc("GET /api/status", s.status)
	mux.HandleFunc("GET /api/models", s.models)
	mux.HandleFunc("POST /api/models/load", s.modelAction("load"))
	mux.HandleFunc("POST /api/models/unload", s.modelAction("unload"))
	mux.HandleFunc("GET /api/events", s.events)
	mux.HandleFunc("GET /api/logs", s.logs)
	mux.HandleFunc("GET /api/args", s.args)
	mux.HandleFunc("GET /api/presets", s.getPresets)
	mux.HandleFunc("POST /api/presets/parse", s.parsePresets)
	mux.HandleFunc("POST /api/presets/edit", s.editPresets)
	mux.HandleFunc("POST /api/presets/preview", s.previewPresets)
	mux.HandleFunc("POST /api/presets/apply", s.applyPresets)
	mux.HandleFunc("GET /api/presets/backups", s.listBackups)
	mux.HandleFunc("GET /api/presets/backups/{name}", s.getBackup)
	mux.HandleFunc("GET /api/applies", s.listApplies)
	mux.HandleFunc("GET /api/applies/{id}", s.getApply)
	mux.HandleFunc("POST /api/service/restart", s.restart)
	mux.HandleFunc("GET /api/hf/search", s.hfSearch)
	mux.HandleFunc("GET /api/hf/repo", s.hfRepo)
	mux.HandleFunc("GET /api/local", s.local)
	mux.HandleFunc("GET /api/recommend", s.recommendations)
	mux.HandleFunc("POST /api/models/download", s.download)
	mux.HandleFunc("POST /api/models/delete", s.deleteModel)
	mux.HandleFunc("POST /api/vram", s.vramEstimate)
	mux.HandleFunc("POST /api/vram/fit", s.vramFit)
	mux.HandleFunc("GET /api/llamacpp", s.llamacppStatus)
	mux.HandleFunc("GET /api/llamacpp/releases", s.llamacppReleases)
	mux.HandleFunc("GET /api/llamacpp/update", s.llamacppUpdate)
	mux.HandleFunc("GET /api/llamacpp/versions", s.llamacppVersions)
	mux.HandleFunc("GET /api/llamacpp/log", s.llamacppLog)
	mux.HandleFunc("POST /api/llamacpp/{op}", s.llamacppOp)
	mux.Handle("POST /api/chat", http.MaxBytesHandler(s.routerProxy("/v1/chat/completions"), 32<<20))
	mux.Handle("POST /api/embeddings", http.MaxBytesHandler(s.routerProxy("/v1/embeddings"), 4<<20))
	mux.HandleFunc("GET /api/router/settings", s.getRouterSettings)
	mux.HandleFunc("POST /api/router/settings", s.setRouterSetting)
	mux.HandleFunc("GET /api/router/key", s.getRouterKey)
	mux.HandleFunc("GET /api/gpu/power", s.getPower)
	mux.HandleFunc("POST /api/gpu/power", s.setPower)
	mux.HandleFunc("GET /api/schedules", s.getSchedules)
	mux.HandleFunc("PUT /api/schedules", s.putSchedules)
	mux.HandleFunc("POST /api/schedules/{id}/run", s.runSchedule)
	mux.HandleFunc("GET /api/usage", s.usage)
	mux.HandleFunc("GET /api/usage/cleanup", s.usageCleanup)
	mux.HandleFunc("GET /api/alerts", s.getAlerts)
	mux.HandleFunc("PUT /api/alerts/settings", s.putAlertSettings)
	mux.HandleFunc("POST /api/alerts/test", s.testAlert)
	mux.HandleFunc("GET /api/sweep", s.getSweep)
	mux.HandleFunc("POST /api/sweep", s.startSweep)
	mux.HandleFunc("POST /api/sweep/cancel", s.cancelSweep)
	mux.HandleFunc("POST /api/bench", s.runBench)
	mux.HandleFunc("GET /api/bench", s.listBench)
	mux.HandleFunc("DELETE /api/bench/{id}", s.deleteBench)
	mux.HandleFunc("GET /api/doctor", s.doctor)
	mux.HandleFunc("GET /api/export", s.export)
	mux.HandleFunc("POST /api/import", s.importBundle)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, http.StatusNotFound, "no such endpoint: %s %s", r.Method, r.URL.Path)
	})
	return requireJSON(mux)
}

// requireJSON rejects state-changing requests that are not JSON. Browsers can send
// form/text POSTs to another origin without asking (CSRF), but a cross-origin
// application/json request needs a CORS preflight, which this server never approves.
func requireJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			ct := r.Header.Get("Content-Type")
			if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(ct)), "application/json") {
				writeErr(w, http.StatusUnsupportedMediaType, "requests that change state must be application/json")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, format string, a ...any) {
	writeJSON(w, code, map[string]string{"error": fmt.Sprintf(format, a...)})
}

func readJSON(r *http.Request, v any) error {
	return json.NewDecoder(http.MaxBytesReader(nil, r.Body, 4<<20)).Decode(v)
}

func routerErr(w http.ResponseWriter, err error) {
	var ae *llamacpp.APIError
	if errors.As(err, &ae) {
		writeErr(w, http.StatusBadGateway, "%s", ae.Message)
		return
	}
	writeErr(w, http.StatusBadGateway, "router unreachable: %v", err)
}

func (s *Server) info(w http.ResponseWriter, r *http.Request) {
	host, _ := os.Hostname()
	writeJSON(w, 200, map[string]any{
		"version":     s.Version,
		"hostname":    host,
		"preset_file": s.Cfg.PresetFile,
		"router_url":  s.Cfg.Router.URL,
		"service":     s.Cfg.Router.Service,
		"models_dir":  s.Cfg.Router.ModelsDir,
		"read_only":   readOnly(r),
	})
}

type statusResp struct {
	Router struct {
		Healthy bool            `json:"healthy"`
		Error   string          `json:"error,omitempty"`
		Props   json.RawMessage `json:"props,omitempty"`
	} `json:"router"`
	Service  system.ServiceStatus `json:"service"`
	GPUs     []gpu.GPU            `json:"gpus"`
	Devices  []string             `json:"devices"` // llama.cpp device name per GPU (CUDA0, ROCm0, …)
	Host     *hoststat.Host       `json:"host,omitempty"`
	GPUError string               `json:"gpu_error,omitempty"`
	Time     time.Time            `json:"time"`
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	var resp statusResp
	resp.Time = time.Now()
	if err := s.Router.Health(ctx); err != nil {
		resp.Router.Error = err.Error()
	} else {
		resp.Router.Healthy = true
		resp.Router.Props, _ = s.Router.Props(ctx)
	}
	resp.Service, _ = system.Status(ctx, s.Cfg.Router.Service)
	gpus, err := gpu.Read(ctx)
	if err != nil {
		resp.GPUError = err.Error()
	}
	resp.GPUs = gpus
	resp.Devices = s.deviceNames(ctx, gpus, false)
	resp.Host = s.Host.Latest()
	writeJSON(w, 200, resp)
}

func (s *Server) models(w http.ResponseWriter, r *http.Request) {
	raw, err := s.Router.ModelsRaw(r.Context())
	if err != nil {
		routerErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(raw)
}

func (s *Server) modelAction(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string `json:"model"`
		}
		if err := readJSON(r, &req); err != nil || req.Model == "" {
			writeErr(w, 400, "body must be {\"model\": \"<id>\"}")
			return
		}
		var err error
		if action == "load" {
			err = s.Router.Load(r.Context(), req.Model)
		} else {
			err = s.Router.Unload(r.Context(), req.Model)
		}
		if err != nil {
			routerErr(w, err)
			return
		}
		s.Log.Info("model "+action, "model", req.Model)
		writeJSON(w, 200, map[string]bool{"success": true})
	}
}

// sse prepares a Server-Sent Events response and returns a send function.
func sse(w http.ResponseWriter) (func(event string, data any) error, bool) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, 500, "streaming unsupported")
		return nil, false
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	fl.Flush()
	return func(event string, data any) error {
		b, err := json.Marshal(data)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b); err != nil {
			return err
		}
		fl.Flush()
		return nil
	}, true
}

// events streams router model events ("model"), GPU telemetry ("gpu") and router health ("router").
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	send, ok := sse(w)
	if !ok {
		return
	}
	ctx := r.Context()
	out := make(chan [2]any, 64)
	push := func(ev string, data any) {
		select {
		case out <- [2]any{ev, data}:
		default: // slow client: drop
		}
	}
	go func() {
		for ctx.Err() == nil {
			push("router", map[string]any{"connected": true})
			err := s.Router.Events(ctx, func(e llamacpp.Event) { push("model", e) })
			if ctx.Err() != nil {
				return
			}
			push("router", map[string]any{"connected": false, "error": err.Error()})
			select {
			case <-ctx.Done():
			case <-time.After(3 * time.Second):
			}
		}
	}()
	go func() {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			if g, err := gpu.Read(ctx); err == nil {
				push("gpu", g)
			}
			if h := s.Host.Latest(); h != nil {
				push("host", h)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	keepalive := time.NewTicker(20 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case m := <-out:
			if send(m[0].(string), m[1]) != nil {
				return
			}
		case <-keepalive.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			w.(http.Flusher).Flush()
		}
	}
}

func (s *Server) logs(w http.ResponseWriter, r *http.Request) {
	n, _ := strconv.Atoi(r.URL.Query().Get("n"))
	if n <= 0 || n > 5000 {
		n = 200
	}
	follow := r.URL.Query().Get("follow") != ""
	if !follow {
		var lines []string
		err := system.Journal(r.Context(), s.Cfg.Router.Service, n, false, func(l string) { lines = append(lines, l) })
		if err != nil && len(lines) == 0 {
			writeErr(w, 500, "journalctl: %v", err)
			return
		}
		writeJSON(w, 200, map[string]any{"lines": lines})
		return
	}
	send, ok := sse(w)
	if !ok {
		return
	}
	_ = system.Journal(r.Context(), s.Cfg.Router.Service, n, true, func(l string) { send("log", l) })
}

func (s *Server) args(w http.ResponseWriter, r *http.Request) {
	cat, err := s.Catalog.Get(r.Context())
	if err != nil {
		writeErr(w, 500, "%v", err)
		return
	}
	writeJSON(w, 200, map[string]any{
		"version":      cat.Version,
		"args":         cat.Args,
		"preset_only":  llamacpp.PresetOnlyKeys,
		"router_owned": llamacpp.RouterControlledKeys,
	})
}

type sectionJSON struct {
	Name     string       `json:"name"`
	Entries  []presets.KV `json:"entries"`
	Comments []string     `json:"comments"`
}

func sectionsOf(text string) (preamble []presets.KV, sections []sectionJSON) {
	d := presets.Parse(text)
	preamble = d.Preamble.Entries()
	if preamble == nil {
		preamble = []presets.KV{}
	}
	sections = []sectionJSON{}
	for _, s := range d.Sections {
		e := s.Entries()
		if e == nil {
			e = []presets.KV{}
		}
		c := s.Comments()
		if c == nil {
			c = []string{}
		}
		sections = append(sections, sectionJSON{s.Name, e, c})
	}
	return
}

func (s *Server) getPresets(w http.ResponseWriter, r *http.Request) {
	text, hash, err := s.Apply.Read()
	if err != nil {
		writeErr(w, 500, "%v", err)
		return
	}
	pre, secs := sectionsOf(text)
	backups := s.Apply.Backups()
	if backups == nil {
		backups = []string{}
	}
	writeJSON(w, 200, map[string]any{
		"path": s.Cfg.PresetFile, "text": text, "hash": hash,
		"preamble": pre, "sections": secs, "backups": backups,
	})
}

func (s *Server) parsePresets(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text string `json:"text"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "%v", err)
		return
	}
	pre, secs := sectionsOf(req.Text)
	writeJSON(w, 200, map[string]any{"text": req.Text, "preamble": pre, "sections": secs})
}

// editPresets applies a structured edit to a working copy (not to disk), keeping comments
// and layout. Keys are canonicalised so that e.g. "c" and "ctx-size" are the same entry.
func (s *Server) editPresets(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text    string       `json:"text"`
		Op      string       `json:"op"` // upsert, delete, rename
		Section string       `json:"section"`
		Entries []presets.KV `json:"entries"`
		NewName string       `json:"new_name"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "%v", err)
		return
	}
	req.Section = strings.TrimSpace(req.Section)
	if req.Section == "" || strings.ContainsAny(req.Section, "[]\n") {
		writeErr(w, 400, "invalid section name %q", req.Section)
		return
	}
	cat, err := s.Catalog.Get(r.Context())
	if err != nil {
		writeErr(w, 500, "%v", err)
		return
	}
	d := presets.Parse(req.Text)
	if _, ok := d.Preamble.Get("version"); !ok {
		d.SetPreamble("version", "1")
	}
	switch req.Op {
	case "upsert":
		var kvs []presets.KV
		for _, kv := range req.Entries {
			kv.Key, kv.Value = strings.TrimSpace(kv.Key), strings.TrimSpace(kv.Value)
			if kv.Key != "" {
				kvs = append(kvs, kv)
			}
		}
		d.Upsert(req.Section, kvs, cat.Canonical)
	case "delete":
		if !d.Delete(req.Section) {
			writeErr(w, 404, "no section [%s]", req.Section)
			return
		}
	case "rename":
		nn := strings.TrimSpace(req.NewName)
		if nn == "" || strings.ContainsAny(nn, "[]\n") || d.Section(nn) != nil {
			writeErr(w, 400, "invalid or existing name %q", nn)
			return
		}
		if !d.Rename(req.Section, nn) {
			writeErr(w, 404, "no section [%s]", req.Section)
			return
		}
	default:
		writeErr(w, 400, "unknown op %q", req.Op)
		return
	}
	text := d.String()
	pre, secs := sectionsOf(text)
	writeJSON(w, 200, map[string]any{"text": text, "preamble": pre, "sections": secs})
}

func (s *Server) previewPresets(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text string `json:"text"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "%v", err)
		return
	}
	p, err := s.Apply.Preview(r.Context(), req.Text)
	if err != nil {
		writeErr(w, 502, "%v", err)
		return
	}
	writeJSON(w, 200, map[string]any{"preview": p, "ok": p.OK()})
}

func (s *Server) applyPresets(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text     string `json:"text"`
		BaseHash string `json:"base_hash"`
		Summary  string `json:"summary"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "%v", err)
		return
	}
	// The apply must finish even if the client goes away (e.g. SSH tunnel drops).
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 3*time.Minute)
	defer cancel()
	res, err := s.Apply.Apply(ctx, req.Text, req.BaseHash, req.Summary)
	if errors.Is(err, apply.ErrConflict) {
		writeErr(w, 409, "%v", err)
		return
	}
	if err != nil {
		writeErr(w, 500, "%v", err)
		return
	}
	s.Log.Info("apply", "ok", res.OK, "error", res.Error, "rolled_back", res.RolledBack, "id", res.ApplyID)
	writeJSON(w, 200, res)
}

func (s *Server) listApplies(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListApplies(100)
	if err != nil {
		writeErr(w, 500, "%v", err)
		return
	}
	if list == nil {
		list = []store.Apply{}
	}
	writeJSON(w, 200, list)
}

func (s *Server) getApply(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	a, err := s.Store.GetApply(id)
	if err != nil {
		writeErr(w, 404, "apply %d not found", id)
		return
	}
	writeJSON(w, 200, struct {
		store.Apply
		Diff []presets.DiffLine `json:"diff"`
	}{a, presets.Diff(a.Before, a.After)})
}

func (s *Server) restart(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 2*time.Minute)
	defer cancel()
	if err := system.Restart(ctx, s.Cfg.Router.Service); err != nil {
		writeErr(w, 500, "%v", err)
		return
	}
	for i := 0; i < 60; i++ {
		if s.Router.Health(ctx) == nil {
			s.Log.Info("router restarted")
			writeJSON(w, 200, map[string]bool{"success": true})
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	writeErr(w, 504, "service restarted but router is not healthy yet")
}
