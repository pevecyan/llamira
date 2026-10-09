// Package metrics exposes Prometheus metrics (text format, no client library).
package metrics

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"llamira/internal/gpu"
	"llamira/internal/hoststat"
	"llamira/internal/llamacpp"
	"llamira/internal/models"
	"llamira/internal/usage"
)

type counters struct{ requests, promptTokens, genTokens float64 }

// Collector serves /metrics and counts requests seen by the usage collector.
type Collector struct {
	Router    *llamacpp.Client
	ModelsDir string
	Host      func() *hoststat.Host // latest host snapshot (optional)
	start     time.Time
	mu        sync.Mutex
	perModel  map[string]*counters
}

func New(router *llamacpp.Client, modelsDir string) *Collector {
	return &Collector{Router: router, ModelsDir: modelsDir, start: time.Now(), perModel: map[string]*counters{}}
}

// Observe counts requests that finished after the process started (the journal backfill is
// not counted, so counters start at zero like any Prometheus counter after a restart).
func (c *Collector) Observe(rs []usage.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, r := range rs {
		if r.Time.Before(c.start) {
			continue
		}
		k := c.perModel[r.Model]
		if k == nil {
			k = &counters{}
			c.perModel[r.Model] = k
		}
		k.requests++
		k.promptTokens += float64(r.PromptN)
		k.genTokens += float64(r.GenN)
	}
}

func esc(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s)
}

type writer struct {
	w    io.Writer
	seen map[string]bool
}

func (w *writer) metric(name, typ, help string, value float64, labels ...string) {
	if !w.seen[name] {
		fmt.Fprintf(w.w, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
		w.seen[name] = true
	}
	var l []string
	for i := 0; i+1 < len(labels); i += 2 {
		l = append(l, fmt.Sprintf(`%s="%s"`, labels[i], esc(labels[i+1])))
	}
	if len(l) > 0 {
		fmt.Fprintf(w.w, "%s{%s} %g\n", name, strings.Join(l, ","), value)
	} else {
		fmt.Fprintf(w.w, "%s %g\n", name, value)
	}
}

func (c *Collector) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	rw.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w := &writer{w: rw, seen: map[string]bool{}}

	up := 0.0
	if c.Router.Health(ctx) == nil {
		up = 1
	}
	w.metric("llama_router_up", "gauge", "1 if the llama-server router answers /health", up)
	if list, err := c.Router.Models(ctx, false); err == nil {
		for _, m := range list {
			loaded := 0.0
			if m.Status.Value == "loaded" || m.Status.Value == "sleeping" {
				loaded = 1
			}
			w.metric("llama_model_loaded", "gauge", "1 if the model is loaded", loaded, "model", m.ID)
		}
		for _, m := range list {
			w.metric("llama_model_status", "gauge", "model status (1 for the current status)", 1, "model", m.ID, "status", m.Status.Value)
		}
	}
	if gs, err := gpu.Read(ctx); err == nil {
		for _, g := range gs {
			idx := fmt.Sprint(g.Index)
			w.metric("llama_gpu_memory_used_bytes", "gauge", "GPU memory used", float64(g.MemUsedMiB)*(1<<20), "gpu", idx, "name", g.Name)
		}
		for _, g := range gs {
			w.metric("llama_gpu_memory_total_bytes", "gauge", "GPU memory total", float64(g.MemTotalMiB)*(1<<20), "gpu", fmt.Sprint(g.Index))
		}
		for _, g := range gs {
			w.metric("llama_gpu_utilization_ratio", "gauge", "GPU utilisation 0..1", float64(g.UtilPct)/100, "gpu", fmt.Sprint(g.Index))
		}
		for _, g := range gs {
			w.metric("llama_gpu_temperature_celsius", "gauge", "GPU temperature", float64(g.TempC), "gpu", fmt.Sprint(g.Index))
		}
		for _, g := range gs {
			w.metric("llama_gpu_power_watts", "gauge", "GPU power draw", g.PowerW, "gpu", fmt.Sprint(g.Index))
		}
		for _, g := range gs {
			w.metric("llama_gpu_power_limit_watts", "gauge", "GPU power limit", g.PowerLimitW, "gpu", fmt.Sprint(g.Index))
		}
	}
	if c.Host != nil {
		if h := c.Host(); h != nil {
			w.metric("llama_host_memory_total_bytes", "gauge", "host RAM", float64(h.MemTotal))
			w.metric("llama_host_memory_used_bytes", "gauge", "host RAM in use (total - available)", float64(h.MemUsed))
			w.metric("llama_host_memory_available_bytes", "gauge", "host RAM available", float64(h.MemAvail))
			w.metric("llama_host_swap_total_bytes", "gauge", "swap size", float64(h.SwapTotal))
			w.metric("llama_host_swap_used_bytes", "gauge", "swap in use", float64(h.SwapUsed))
			w.metric("llama_host_cpu_utilization_ratio", "gauge", "host CPU busy 0..1 (all cores)", h.CPUPct/100)
			w.metric("llama_host_load1", "gauge", "1-minute load average", h.Load[0])
			if h.CPUTempC > 0 {
				w.metric("llama_host_cpu_temperature_celsius", "gauge", "CPU package temperature", h.CPUTempC)
			}
			if u := h.Unit; u != nil {
				w.metric("llama_server_memory_bytes", "gauge", "memory of the llama-server unit (router + models)", float64(u.Mem), "unit", u.Name, "kind", "total")
				w.metric("llama_server_memory_bytes", "gauge", "", float64(u.Anon), "unit", u.Name, "kind", "anon")
				w.metric("llama_server_memory_bytes", "gauge", "", float64(u.File), "unit", u.Name, "kind", "file")
				w.metric("llama_server_cpu_utilization_ratio", "gauge", "CPU of the llama-server unit 0..1 (of all cores)", u.CPUPct/100, "unit", u.Name)
			}
		}
	}
	if c.ModelsDir != "" {
		if d, err := models.DiskUsage(c.ModelsDir); err == nil {
			w.metric("llama_models_disk_free_bytes", "gauge", "free space on the models filesystem", float64(d.Free), "path", d.Path)
			w.metric("llama_models_disk_total_bytes", "gauge", "size of the models filesystem", float64(d.Total), "path", d.Path)
		}
	}
	c.mu.Lock()
	names := make([]string, 0, len(c.perModel))
	for n := range c.perModel {
		names = append(names, n)
	}
	sort.Strings(names)
	snap := map[string]counters{}
	for _, n := range names {
		snap[n] = *c.perModel[n]
	}
	c.mu.Unlock()
	for _, n := range names {
		w.metric("llama_requests_total", "counter", "completed requests since llamira started", snap[n].requests, "model", n)
	}
	for _, n := range names {
		w.metric("llama_prompt_tokens_total", "counter", "prompt tokens processed", snap[n].promptTokens, "model", n)
	}
	for _, n := range names {
		w.metric("llama_generated_tokens_total", "counter", "tokens generated", snap[n].genTokens, "model", n)
	}
}
