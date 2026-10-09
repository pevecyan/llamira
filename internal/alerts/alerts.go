// Package alerts watches the router, models, GPUs, disk, schedules and llama.cpp releases and
// delivers events to a webhook (e.g. an n8n workflow), ntfy and/or Telegram, with per-event
// cooldowns.
package alerts

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"llamira/internal/gpu"
	"llamira/internal/hoststat"
	"llamira/internal/llamacpp"
	"llamira/internal/models"
)

// Settings are edited in the UI and stored in the database.
type Settings struct {
	WebhookURL     string `json:"webhook_url"`
	NtfyURL        string `json:"ntfy_url"`   // topic URL, e.g. https://ntfy.sh/my-llama-alerts
	NtfyToken      string `json:"ntfy_token"` // optional access token (protected topics)
	TelegramToken  string `json:"telegram_token"`
	TelegramChat   string `json:"telegram_chat"`
	RouterDown     bool   `json:"router_down"`
	ModelFailed    bool   `json:"model_failed"`
	Downloads      bool   `json:"downloads"`
	ScheduleFailed bool   `json:"schedule_failed"`
	NewRelease     bool   `json:"new_release"`  // a newer llama.cpp release than the running build
	GPUTempC       int    `json:"gpu_temp_c"`   // 0 = off
	GPUVRAMPct     int    `json:"gpu_vram_pct"` // 0 = off
	RAMPct         int    `json:"ram_pct"`      // host RAM in use, percent; 0 = off
	SwapMB         int    `json:"swap_mb"`      // swap in use at or above this; 0 = off
	DiskFreeGB     int    `json:"disk_free_gb"` // 0 = off
	CooldownMin    int    `json:"cooldown_min"` // per event key
}

func DefaultSettings() Settings {
	return Settings{RouterDown: true, ModelFailed: true, Downloads: true, ScheduleFailed: true, GPUTempC: 85, DiskFreeGB: 20, CooldownMin: 30}
}

// Event is what gets POSTed.
type Event struct {
	Time     time.Time      `json:"time"`
	Host     string         `json:"host"`
	Event    string         `json:"event"`    // router_down, router_up, model_failed, download_finished, download_failed, gpu_temp, gpu_vram, ram_high, swap_used, disk_low, schedule_failed, llamacpp_release, test
	Severity string         `json:"severity"` // info, warning, critical
	Message  string         `json:"message"`
	Model    string         `json:"model,omitempty"`
	Data     map[string]any `json:"data,omitempty"`
	Sent     bool           `json:"sent"`               // every configured channel accepted it
	Error    string         `json:"error,omitempty"`    // per-channel failures
	Channels []string       `json:"channels,omitempty"` // channels that accepted it
}

// Store persists settings and the event log.
type Store interface {
	AlertSettings() (Settings, bool)
	AddAlert(Event) error
	Value(key string) string
	SetValue(key, value string) error
}

// Release reports the latest upstream llama.cpp release and whether it is newer than the
// running build.
type Release func(ctx context.Context) (latest, current string, newer bool)

type Monitor struct {
	Router    *llamacpp.Client
	Store     Store
	ModelsDir string
	Log       *slog.Logger
	Release   Release
	Host      func() *hoststat.Host // latest host snapshot (optional)

	mu        sync.Mutex
	last      map[string]time.Time // cooldown per key
	downCount int
	down      bool
	prev      map[string]llamacpp.Model
	host      string
	relAt     time.Time
}

func (m *Monitor) settings() Settings {
	if s, ok := m.Store.AlertSettings(); ok {
		return s
	}
	return DefaultSettings()
}

// Run polls every interval until ctx is done.
func (m *Monitor) Run(ctx context.Context, interval time.Duration) {
	m.last = map[string]time.Time{}
	m.host, _ = os.Hostname()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		m.check(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (m *Monitor) check(ctx context.Context) {
	s := m.settings()
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// router health (2 consecutive failures = down)
	if err := m.Router.Health(cctx); err != nil {
		m.downCount++
		if m.downCount == 2 && !m.down {
			m.down = true
			if s.RouterDown {
				m.fire(ctx, s, Event{Event: "router_down", Severity: "critical", Message: "llama-server router is not responding: " + err.Error()}, "router")
			}
		}
	} else {
		if m.down && s.RouterDown {
			m.fire(ctx, s, Event{Event: "router_up", Severity: "info", Message: "llama-server router is responding again"}, "")
		}
		m.down, m.downCount = false, 0
	}

	// model transitions
	if list, err := m.Router.Models(cctx, false); err == nil {
		cur := map[string]llamacpp.Model{}
		for _, md := range list {
			cur[md.ID] = md
		}
		if m.prev != nil {
			for id, md := range cur {
				p, had := m.prev[id]
				if s.ModelFailed && md.Status.Failed && (!had || !p.Status.Failed) {
					code := 0
					if md.Status.ExitCode != nil {
						code = *md.Status.ExitCode
					}
					m.fire(ctx, s, Event{Event: "model_failed", Severity: "critical", Model: id,
						Message: fmt.Sprintf("model %s failed to load (exit code %d)", id, code), Data: map[string]any{"exit_code": code}}, "failed:"+id)
				}
				if s.Downloads && had && p.Status.Value == "downloading" && md.Status.Value != "downloading" {
					m.fire(ctx, s, Event{Event: "download_finished", Severity: "info", Model: id, Message: "download finished: " + id}, "")
				}
			}
			for id, p := range m.prev {
				if _, ok := cur[id]; !ok && s.Downloads && p.Status.Value == "downloading" {
					m.fire(ctx, s, Event{Event: "download_failed", Severity: "warning", Model: id, Message: "download failed or was cancelled: " + id}, "")
				}
			}
		}
		m.prev = cur
	}

	// GPU temperature and memory
	if s.GPUTempC > 0 || s.GPUVRAMPct > 0 {
		if gs, err := gpu.Read(cctx); err == nil {
			for _, g := range gs {
				if s.GPUTempC > 0 && g.TempC >= s.GPUTempC {
					m.fire(ctx, s, Event{Event: "gpu_temp", Severity: "warning",
						Message: fmt.Sprintf("GPU %d (%s) at %d °C (threshold %d °C)", g.Index, g.Name, g.TempC, s.GPUTempC),
						Data:    map[string]any{"gpu": g.Index, "temp_c": g.TempC, "power_w": g.PowerW}}, fmt.Sprintf("gpu_temp:%d", g.Index))
				}
				if s.GPUVRAMPct > 0 && g.MemTotalMiB > 0 && g.MemUsedMiB*100 >= s.GPUVRAMPct*g.MemTotalMiB {
					pct := g.MemUsedMiB * 100 / g.MemTotalMiB
					m.fire(ctx, s, Event{Event: "gpu_vram", Severity: "warning",
						Message: fmt.Sprintf("GPU %d (%s) memory at %d%% (%d of %d MiB, threshold %d%%)", g.Index, g.Name, pct, g.MemUsedMiB, g.MemTotalMiB, s.GPUVRAMPct),
						Data:    map[string]any{"gpu": g.Index, "used_mib": g.MemUsedMiB, "total_mib": g.MemTotalMiB}}, fmt.Sprintf("gpu_vram:%d", g.Index))
				}
			}
		}
	}

	// host RAM and swap
	if m.Host != nil && (s.RAMPct > 0 || s.SwapMB > 0) {
		if h := m.Host(); h != nil && h.MemTotal > 0 {
			gib := func(b uint64) float64 { return float64(b) / (1 << 30) }
			if pct := int(h.MemUsed * 100 / h.MemTotal); s.RAMPct > 0 && pct >= s.RAMPct {
				data := map[string]any{"used": h.MemUsed, "total": h.MemTotal, "available": h.MemAvail}
				if h.Unit != nil {
					data["llama_server"] = h.Unit.Mem
				}
				m.fire(ctx, s, Event{Event: "ram_high", Severity: "warning",
					Message: fmt.Sprintf("host RAM at %d%% (%.1f of %.1f GiB, threshold %d%%)", pct, gib(h.MemUsed), gib(h.MemTotal), s.RAMPct), Data: data}, "ram")
			}
			if s.SwapMB > 0 && h.SwapUsed >= uint64(s.SwapMB)<<20 {
				m.fire(ctx, s, Event{Event: "swap_used", Severity: "warning",
					Message: fmt.Sprintf("%.1f GiB of swap in use (threshold %d MiB): models are paged out, expect slow requests", gib(h.SwapUsed), s.SwapMB),
					Data:    map[string]any{"swap_used": h.SwapUsed, "swap_total": h.SwapTotal}}, "swap")
			}
		}
	}

	// new llama.cpp release: once per release tag (remembered across restarts)
	if s.NewRelease && m.Release != nil && time.Since(m.relAt) > 10*time.Minute {
		m.relAt = time.Now()
		if latest, current, newer := m.Release(cctx); newer && latest != m.Store.Value("alert_release") {
			_ = m.Store.SetValue("alert_release", latest)
			m.fire(ctx, s, Event{Event: "llamacpp_release", Severity: "info",
				Message: fmt.Sprintf("llama.cpp %s is available (running %s)", latest, current),
				Data:    map[string]any{"latest": latest, "current": current}}, "")
		}
	}

	// disk space
	if s.DiskFreeGB > 0 && m.ModelsDir != "" {
		if d, err := models.DiskUsage(m.ModelsDir); err == nil && d.Free < uint64(s.DiskFreeGB)<<30 {
			m.fire(ctx, s, Event{Event: "disk_low", Severity: "warning",
				Message: fmt.Sprintf("only %.1f GB free on %s (threshold %d GB)", float64(d.Free)/(1<<30), d.Path, s.DiskFreeGB),
				Data:    map[string]any{"free_bytes": d.Free, "path": d.Path}}, "disk")
		}
	}
}

// fire records and sends an event; key ("" = no cooldown) suppresses repeats within the cooldown.
func (m *Monitor) fire(ctx context.Context, s Settings, e Event, key string) {
	m.mu.Lock()
	if key != "" {
		cd := time.Duration(max(s.CooldownMin, 1)) * time.Minute
		if t, ok := m.last[key]; ok && time.Since(t) < cd {
			m.mu.Unlock()
			return
		}
		m.last[key] = time.Now()
	}
	m.mu.Unlock()
	m.Send(ctx, s, e)
}

// ScheduleResult is called after a scheduled rule ran (not for "Run now" from the UI).
func (m *Monitor) ScheduleResult(ctx context.Context, rule, action, model, result string) {
	if !strings.Contains(result, " failed: ") {
		return
	}
	s := m.settings()
	if !s.ScheduleFailed {
		return
	}
	m.fire(ctx, s, Event{Event: "schedule_failed", Severity: "warning", Model: model,
		Message: "scheduled " + result, Data: map[string]any{"rule": rule, "action": action}}, "schedule:"+rule)
}

// Send delivers an event to every configured channel and stores it.
func (m *Monitor) Send(ctx context.Context, s Settings, e Event) Event {
	e.Time = time.Now()
	if e.Host == "" {
		e.Host = m.host
		if e.Host == "" {
			e.Host, _ = os.Hostname()
		}
	}
	ch := channels(s)
	if len(ch) == 0 {
		e.Error = "no delivery channel configured"
	} else {
		names := make([]string, 0, len(ch))
		for n := range ch {
			names = append(names, n)
		}
		sort.Strings(names)
		var errs []string
		for _, n := range names {
			cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := ch[n](cctx, e)
			cancel()
			if err != nil {
				errs = append(errs, n+": "+err.Error())
			} else {
				e.Channels = append(e.Channels, n)
			}
		}
		e.Sent = len(errs) == 0
		e.Error = strings.Join(errs, "; ")
	}
	if m.Log != nil {
		m.Log.Info("alert", "event", e.Event, "message", e.Message, "sent", e.Sent, "channels", e.Channels, "error", e.Error)
	}
	_ = m.Store.AddAlert(e)
	return e
}
