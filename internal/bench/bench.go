// Package bench runs a reproducible speed test of a model through the llama.cpp router:
// load time, prompt processing and generation speed (llama.cpp's own timings), and GPU
// peak memory / power / temperature sampled while it runs.
package bench

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"llamira/internal/gpu"
	"llamira/internal/hoststat"
	"llamira/internal/llamacpp"
)

// Request describes a benchmark run.
type Request struct {
	Model         string `json:"model"`
	PromptTokens  int    `json:"prompt_tokens"`  // approximate prompt length
	GenTokens     int    `json:"gen_tokens"`     // tokens to generate (EOS ignored)
	RestoreLoaded bool   `json:"restore_loaded"` // reload previously loaded model(s) afterwards
}

// GPUStat is what was observed on one GPU during the run.
type GPUStat struct {
	Index      int     `json:"index"`
	PeakMiB    int     `json:"peak_mib"`
	MaxTempC   int     `json:"max_temp_c"`
	AvgPowerW  float64 `json:"avg_power_w"`
	MaxPowerW  float64 `json:"max_power_w"`
	PowerLimit float64 `json:"power_limit_w"`
	AvgClock   float64 `json:"avg_clock_mhz"` // SM clock while measuring
	MaxFanPct  int     `json:"max_fan_pct"`   // -1 if not reported
}

// Result of a run.
type Result struct {
	Model        string    `json:"model"`
	Preset       string    `json:"preset"`          // effective config reported by the router
	Build        string    `json:"build,omitempty"` // llama.cpp build of the router (b11036-911f6cdc8)
	Time         time.Time `json:"time"`
	LoadMS       int64     `json:"load_ms"` // 0 if the model was already loaded
	PromptN      int       `json:"prompt_n"`
	PromptTPS    float64   `json:"prompt_tps"`
	GenN         int       `json:"gen_n"`
	GenTPS       float64   `json:"gen_tps"`
	TTFTMS       float64   `json:"ttft_ms"` // prompt processing time ≈ time to first token
	TotalMS      int64     `json:"total_ms"`
	GPUs         []GPUStat `json:"gpus"`
	Error        string    `json:"error,omitempty"`
	RestoredWith []string  `json:"restored,omitempty"`
	PromptReq    int       `json:"prompt_tokens,omitempty"` // requested sizes (to re-run the same test)
	GenReq       int       `json:"gen_tokens,omitempty"`
	Host         *HostStat `json:"host,omitempty"` // host RAM / CPU during the run
}

// HostStat is what was observed on the host during a run.
type HostStat struct {
	MemTotal     uint64  `json:"mem_total"`
	PeakMemUsed  uint64  `json:"peak_mem_used"`            // host RAM in use (total − available)
	PeakUnitMem  uint64  `json:"peak_unit_mem,omitempty"`  // llama-server unit (router + model)
	PeakSwapUsed uint64  `json:"peak_swap_used,omitempty"` // swap in use: the run was probably slowed by paging
	AvgCPUPct    float64 `json:"avg_cpu_pct"`              // host CPU while measuring
	MaxCPUPct    float64 `json:"max_cpu_pct"`
}

// Runner runs one benchmark at a time.
type Runner struct {
	Router *llamacpp.Client
	Host   func() *hoststat.Host // latest host snapshot (optional)
	mu     sync.Mutex
	busy   bool
}

var ErrBusy = errors.New("a benchmark is already running")

// Run executes a benchmark. It blocks until done (typically 10–60 s, longer if the
// model has to be loaded).
func (r *Runner) Run(ctx context.Context, req Request) (*Result, error) {
	if !r.acquire() {
		return nil, ErrBusy
	}
	defer r.release()
	return r.run(ctx, req)
}

func (r *Runner) acquire() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.busy {
		return false
	}
	r.busy = true
	return true
}

func (r *Runner) release() {
	r.mu.Lock()
	r.busy = false
	r.mu.Unlock()
}

// run executes a benchmark; the caller holds the lock.
func (r *Runner) run(ctx context.Context, req Request) (*Result, error) {
	if req.PromptTokens <= 0 {
		req.PromptTokens = 512
	}
	if req.GenTokens <= 0 {
		req.GenTokens = 128
	}
	req.PromptTokens = min(req.PromptTokens, 65536)
	req.GenTokens = min(req.GenTokens, 2048)

	res := &Result{Model: req.Model, Time: time.Now(), PromptReq: req.PromptTokens, GenReq: req.GenTokens}
	start := time.Now()
	models, err := r.Router.Models(ctx, false)
	if err != nil {
		return nil, err
	}
	var target *llamacpp.Model
	var previously []string
	for i := range models {
		if models[i].ID == req.Model {
			target = &models[i]
		}
		if models[i].Status.Value == "loaded" || models[i].Status.Value == "sleeping" {
			previously = append(previously, models[i].ID)
		}
	}
	if target == nil {
		return nil, fmt.Errorf("model %s not found", req.Model)
	}
	res.Preset = target.Status.Preset
	if raw, err := r.Router.Props(ctx); err == nil {
		var p struct {
			BuildInfo string `json:"build_info"`
		}
		if json.Unmarshal(raw, &p) == nil {
			res.Build = p.BuildInfo
		}
	}

	// sample GPUs for the whole run
	sampler := startSampler(ctx)
	defer func() { res.GPUs = sampler.stop() }()
	if r.Host != nil {
		stopHost := trackHost(r.Host, &sampler.measuring)
		defer func() { res.Host = stopHost() }()
	}

	// load
	if target.Status.Value != "loaded" {
		t := time.Now()
		if target.Status.Value != "loading" {
			if err := r.Router.Load(ctx, req.Model); err != nil {
				return failed(res, start, "load: %v", err), nil
			}
		}
		if err := r.waitLoaded(ctx, req.Model, 10*time.Minute); err != nil {
			return failed(res, start, "load: %v", err), nil
		}
		res.LoadMS = time.Since(t).Milliseconds()
	}

	// warm-up, then the measured run
	if _, err := r.complete(ctx, req.Model, "Hello", 4); err != nil {
		return failed(res, start, "warm-up: %v", err), nil
	}
	sampler.measuring.Store(true) // average power only over the measured request
	tm, err := r.complete(ctx, req.Model, Prompt(req.PromptTokens), req.GenTokens)
	sampler.measuring.Store(false)
	if err != nil {
		return failed(res, start, "run: %v", err), nil
	}
	res.PromptN, res.PromptTPS, res.TTFTMS = tm.PromptN, tm.PromptPerSecond, tm.PromptMS
	res.GenN, res.GenTPS = tm.PredictedN, tm.PredictedPerSecond
	res.TotalMS = time.Since(start).Milliseconds()

	if req.RestoreLoaded {
		for _, id := range previously {
			if id != req.Model {
				if r.Router.Load(context.WithoutCancel(ctx), id) == nil {
					res.RestoredWith = append(res.RestoredWith, id)
				}
			}
		}
	}
	return res, nil
}

func failed(res *Result, start time.Time, format string, a ...any) *Result {
	res.Error = fmt.Sprintf(format, a...)
	res.TotalMS = time.Since(start).Milliseconds()
	return res
}

func (r *Runner) waitLoaded(ctx context.Context, id string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ms, err := r.Router.Models(ctx, false)
		if err == nil {
			for _, m := range ms {
				if m.ID != id {
					continue
				}
				switch {
				case m.Status.Value == "loaded":
					return nil
				case m.Status.Failed:
					code := -1
					if m.Status.ExitCode != nil {
						code = *m.Status.ExitCode
					}
					return fmt.Errorf("model failed to load (exit code %d); see the router log", code)
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return errors.New("timed out waiting for the model to load")
}

type timings struct {
	PromptN            int     `json:"prompt_n"`
	PromptMS           float64 `json:"prompt_ms"`
	PromptPerSecond    float64 `json:"prompt_per_second"`
	PredictedN         int     `json:"predicted_n"`
	PredictedPerSecond float64 `json:"predicted_per_second"`
}

// complete calls the router's native /completion (no chat template, prompt cache off,
// EOS ignored so exactly n tokens are generated).
func (r *Runner) complete(ctx context.Context, model, prompt string, n int) (*timings, error) {
	body, _ := json.Marshal(map[string]any{
		"model": model, "prompt": prompt, "n_predict": n, "ignore_eos": true,
		"cache_prompt": false, "temperature": 0, "stream": false,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.Router.BaseURL+"/completion", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if k := r.Router.Key(); k != "" {
		req.Header.Set("Authorization", "Bearer "+k)
	}
	hc := &http.Client{Timeout: 10 * time.Minute}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var out struct {
		Timings timings `json:"timings"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return &out.Timings, nil
}

// Prompt returns a deterministic English text of roughly n tokens (~4.9 characters/token,
// measured with the Qwen3.5 tokenizer; the actual count is reported by llama.cpp).
func Prompt(n int) string {
	const para = "In the mountain village, the old clockmaker repaired watches for travelers who passed " +
		"through on their way to the coast. Each morning he opened the shutters, brewed strong coffee, " +
		"and listened to the ticking of a hundred clocks, every one slightly out of step with the others. " +
		"Visitors asked why he never synchronized them; he answered that time feels different to everyone. "
	var b strings.Builder
	for i := 0; b.Len() < n*49/10; i++ {
		fmt.Fprintf(&b, "%d. %s", i+1, para)
	}
	b.WriteString("\nContinue the story:")
	return b.String()
}

type sampler struct {
	stopc     chan struct{}
	done      chan []GPUStat
	measuring atomic.Bool
}

func startSampler(ctx context.Context) *sampler {
	s := &sampler{stopc: make(chan struct{}), done: make(chan []GPUStat, 1)}
	go func() {
		stats := map[int]*GPUStat{}
		count := map[int]int{}
		var order []int
		t := time.NewTicker(250 * time.Millisecond)
		defer t.Stop()
		for {
			if gs, err := gpu.Read(ctx); err == nil {
				for _, g := range gs {
					st, ok := stats[g.Index]
					if !ok {
						st = &GPUStat{Index: g.Index, PowerLimit: g.PowerLimitW, MaxFanPct: -1}
						stats[g.Index] = st
						order = append(order, g.Index)
					}
					st.PeakMiB = max(st.PeakMiB, g.MemUsedMiB)
					st.MaxTempC = max(st.MaxTempC, g.TempC)
					st.MaxFanPct = max(st.MaxFanPct, g.FanPct)
					st.PowerLimit = g.PowerLimitW
					if s.measuring.Load() {
						st.MaxPowerW = max(st.MaxPowerW, g.PowerW)
						st.AvgPowerW += g.PowerW
						st.AvgClock += float64(g.ClockSM)
						count[g.Index]++
					}
				}
			}
			select {
			case <-s.stopc:
				out := make([]GPUStat, 0, len(order))
				for _, i := range order {
					st := *stats[i]
					if count[i] > 0 {
						st.AvgPowerW /= float64(count[i])
						st.AvgClock /= float64(count[i])
					}
					out = append(out, st)
				}
				s.done <- out
				return
			case <-t.C:
			}
		}
	}()
	return s
}

func (s *sampler) stop() []GPUStat {
	close(s.stopc)
	return <-s.done
}

// trackHost follows the host snapshots (taken every 2 s by the host sampler) during a run:
// peak RAM and swap for the whole run, CPU while measuring. The returned function stops it.
func trackHost(latest func() *hoststat.Host, measuring *atomic.Bool) func() *HostStat {
	stopc := make(chan struct{})
	done := make(chan *HostStat, 1)
	go func() {
		var st *HostStat
		var sum float64
		var n int
		var last time.Time
		t := time.NewTicker(500 * time.Millisecond)
		defer t.Stop()
		for {
			if h := latest(); h != nil && !h.Time.Equal(last) {
				last = h.Time
				if st == nil {
					st = &HostStat{MemTotal: h.MemTotal}
				}
				st.PeakMemUsed = max(st.PeakMemUsed, h.MemUsed)
				st.PeakSwapUsed = max(st.PeakSwapUsed, h.SwapUsed)
				if h.Unit != nil {
					st.PeakUnitMem = max(st.PeakUnitMem, h.Unit.Mem)
				}
				if measuring.Load() {
					sum += h.CPUPct
					n++
					st.MaxCPUPct = max(st.MaxCPUPct, h.CPUPct)
				}
			}
			select {
			case <-stopc:
				if st != nil && n > 0 {
					st.AvgCPUPct = sum / float64(n)
				}
				done <- st
				return
			case <-t.C:
			}
		}
	}()
	return func() *HostStat {
		close(stopc)
		return <-done
	}
}
