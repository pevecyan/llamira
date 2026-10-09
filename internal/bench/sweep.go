package bench

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"sync"
	"time"

	"llamira/internal/gpu"
)

// SweepRequest describes a power-limit sweep.
type SweepRequest struct {
	Model        string  `json:"model"`
	GPUs         []int   `json:"gpus"`
	MinW         float64 `json:"min_w"`
	MaxW         float64 `json:"max_w"`
	StepW        float64 `json:"step_w"`
	PromptTokens int     `json:"prompt_tokens"`
	GenTokens    int     `json:"gen_tokens"`
	SettleSec    int     `json:"settle_sec"`
	MaxTempC     int     `json:"max_temp_c"` // abort above this (0 = 90)
}

// SweepStep is the measurement at one power limit.
type SweepStep struct {
	Watts       float64   `json:"watts"`
	GenTPS      float64   `json:"gen_tps"`
	PromptTPS   float64   `json:"prompt_tps"`
	TTFTMS      float64   `json:"ttft_ms"`
	AvgPowerW   float64   `json:"avg_power_w"` // sum over the selected GPUs, while measuring
	MaxTempC    int       `json:"max_temp_c"`
	AvgClockMHz float64   `json:"avg_clock_mhz"`
	MaxFanPct   int       `json:"max_fan_pct"`
	TokensPerJ  float64   `json:"tokens_per_joule"` // generation tokens/s ÷ watts
	GPUs        []GPUStat `json:"gpus"`
	Error       string    `json:"error,omitempty"`
}

// Pick is a recommended limit.
type Pick struct {
	Watts  float64 `json:"watts"`
	Reason string  `json:"reason"`
}

// Sweep is a (running or finished) power sweep.
type Sweep struct {
	ID       int64            `json:"id"`
	Request  SweepRequest     `json:"request"`
	Preset   string           `json:"preset"`
	Build    string           `json:"build"`
	GPUNames []string         `json:"gpu_names"`
	Original map[int]float64  `json:"original"`
	Started  time.Time        `json:"started"`
	Finished time.Time        `json:"finished,omitempty"`
	Status   string           `json:"status"` // running, done, cancelled, failed
	Error    string           `json:"error,omitempty"`
	Plan     []float64        `json:"plan"`
	Steps    []SweepStep      `json:"steps"`
	Picks    map[string]*Pick `json:"picks,omitempty"` // max, balanced, efficient
}

// SweepStore persists sweeps and the "limits to restore" guard.
type SweepStore interface {
	SetSweepGuard(map[int]float64) error
	SweepGuard() (map[int]float64, bool)
	ClearSweepGuard() error
	AddSweep(*Sweep) (int64, error)
}

// Sweeper runs power sweeps (sharing the benchmark lock).
type Sweeper struct {
	Runner   *Runner
	Store    SweepStore
	SetLimit func(ctx context.Context, gpu int, watts float64) error // temporary, not persisted
	Build    func(ctx context.Context) string

	mu     sync.Mutex
	cur    *Sweep
	cancel context.CancelFunc
}

// Plan returns the list of limits to test.
func (r SweepRequest) Plan() []float64 {
	if r.StepW <= 0 || r.MaxW < r.MinW {
		return nil
	}
	var out []float64
	for w := r.MinW; w <= r.MaxW+0.01; w += r.StepW {
		out = append(out, math.Round(w))
	}
	if last := out[len(out)-1]; last < r.MaxW {
		out = append(out, r.MaxW)
	}
	return out
}

// Current returns a copy of the running or last sweep.
func (s *Sweeper) Current() *Sweep {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur == nil {
		return nil
	}
	c := *s.cur
	c.Steps = slices.Clone(s.cur.Steps)
	return &c
}

// Cancel stops a running sweep (limits are restored by the sweep itself).
func (s *Sweeper) Cancel() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil && s.cur != nil && s.cur.Status == "running" {
		s.cancel()
		return true
	}
	return false
}

// Start validates and launches a sweep in the background.
func (s *Sweeper) Start(req SweepRequest) (*Sweep, error) {
	if req.Model == "" || len(req.GPUs) == 0 {
		return nil, errors.New("model and at least one GPU are required")
	}
	plan := req.Plan()
	if len(plan) == 0 || len(plan) > 40 {
		return nil, errors.New("invalid power range or step (1–40 steps)")
	}
	limits, err := gpu.PowerLimits(context.Background())
	if err != nil {
		return nil, err
	}
	gs, _ := gpu.Read(context.Background())
	orig := map[int]float64{}
	var names []string
	for _, i := range req.GPUs {
		found := false
		for _, l := range limits {
			if l.Index == i {
				found = true
				if req.MinW < l.Min || req.MaxW > l.Max {
					return nil, fmt.Errorf("GPU %d allows %.0f–%.0f W", i, l.Min, l.Max)
				}
				orig[i] = l.Limit
			}
		}
		if !found {
			return nil, fmt.Errorf("no GPU %d", i)
		}
		for _, g := range gs {
			if g.Index == i {
				names = append(names, g.Name)
			}
		}
	}
	if req.PromptTokens <= 0 {
		req.PromptTokens = 8192
	}
	if req.GenTokens <= 0 {
		req.GenTokens = 1024
	}
	if req.SettleSec <= 0 {
		req.SettleSec = 5
	}
	if req.MaxTempC <= 0 {
		req.MaxTempC = 90
	}
	if !s.Runner.acquire() {
		return nil, ErrBusy
	}
	if err := s.Store.SetSweepGuard(orig); err != nil {
		s.Runner.release()
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	sw := &Sweep{Request: req, Original: orig, Started: time.Now(), Status: "running", Plan: plan, GPUNames: names, Steps: []SweepStep{}}
	s.mu.Lock()
	s.cur, s.cancel = sw, cancel
	s.mu.Unlock()
	go s.run(ctx, sw)
	return s.Current(), nil
}

// RecoverLimits restores limits left over from an interrupted sweep (call at startup).
func (s *Sweeper) RecoverLimits(ctx context.Context) {
	if orig, ok := s.Store.SweepGuard(); ok {
		for i, w := range orig {
			_ = s.SetLimit(ctx, i, w)
		}
		_ = s.Store.ClearSweepGuard()
	}
}

func (s *Sweeper) update(f func(*Sweep)) {
	s.mu.Lock()
	f(s.cur)
	s.mu.Unlock()
}

func (s *Sweeper) run(ctx context.Context, sw *Sweep) {
	defer s.Runner.release()
	defer func() {
		// always restore the original limits
		rctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		for i, w := range sw.Original {
			_ = s.SetLimit(rctx, i, w)
		}
		_ = s.Store.ClearSweepGuard()
		s.update(func(x *Sweep) {
			x.Finished = time.Now()
			x.Picks = recommend(x.Steps, x.Original)
		})
		final := s.Current()
		if id, err := s.Store.AddSweep(final); err == nil {
			s.update(func(x *Sweep) { x.ID = id })
		}
	}()
	fail := func(status string, err error) {
		s.update(func(x *Sweep) {
			x.Status = status
			if err != nil {
				x.Error = err.Error()
			}
		})
	}
	if s.Build != nil {
		b := s.Build(ctx)
		s.update(func(x *Sweep) { x.Build = b })
	}
	req := sw.Request
	// load once and warm up (discarded); also records the effective preset
	warm, err := s.Runner.run(ctx, Request{Model: req.Model, PromptTokens: 512, GenTokens: 32})
	if err != nil || warm.Error != "" {
		if err == nil {
			err = errors.New(warm.Error)
		}
		fail("failed", fmt.Errorf("warm-up: %w", err))
		return
	}
	s.update(func(x *Sweep) { x.Preset = warm.Preset })
	for _, w := range sw.Plan {
		if ctx.Err() != nil {
			fail("cancelled", nil)
			return
		}
		for _, i := range req.GPUs {
			if err := s.SetLimit(ctx, i, w); err != nil {
				fail("failed", fmt.Errorf("set %0.f W on GPU %d: %w", w, i, err))
				return
			}
		}
		select {
		case <-ctx.Done():
			fail("cancelled", nil)
			return
		case <-time.After(time.Duration(req.SettleSec) * time.Second):
		}
		res, err := s.Runner.run(ctx, Request{Model: req.Model, PromptTokens: req.PromptTokens, GenTokens: req.GenTokens})
		if ctx.Err() != nil {
			fail("cancelled", nil)
			return
		}
		step := SweepStep{Watts: w, MaxFanPct: -1}
		if err != nil {
			step.Error = err.Error()
		} else if res.Error != "" {
			step.Error = res.Error
		} else {
			step.GenTPS, step.PromptTPS, step.TTFTMS = res.GenTPS, res.PromptTPS, res.TTFTMS
			var clocks float64
			for _, g := range res.GPUs {
				if !slices.Contains(req.GPUs, g.Index) {
					continue
				}
				step.GPUs = append(step.GPUs, g)
				step.AvgPowerW += g.AvgPowerW
				step.MaxTempC = max(step.MaxTempC, g.MaxTempC)
				step.MaxFanPct = max(step.MaxFanPct, g.MaxFanPct)
				clocks += g.AvgClock
			}
			if n := len(step.GPUs); n > 0 {
				step.AvgClockMHz = clocks / float64(n)
			}
			if step.AvgPowerW > 0 {
				step.TokensPerJ = step.GenTPS / step.AvgPowerW
			}
		}
		s.update(func(x *Sweep) { x.Steps = append(x.Steps, step) })
		if step.MaxTempC >= req.MaxTempC {
			fail("failed", fmt.Errorf("stopped: GPU reached %d °C (limit %d °C)", step.MaxTempC, req.MaxTempC))
			return
		}
	}
	s.update(func(x *Sweep) { x.Status = "done" })
}

// recommend picks max performance, balanced (≥95% of max generation speed at the lowest
// limit) and efficient (≥85% at the lowest limit).
func recommend(steps []SweepStep, orig map[int]float64) map[string]*Pick {
	var ok []SweepStep
	for _, s := range steps {
		if s.Error == "" && s.GenTPS > 0 {
			ok = append(ok, s)
		}
	}
	if len(ok) == 0 {
		return nil
	}
	best := ok[0]
	for _, s := range ok {
		if s.GenTPS > best.GenTPS {
			best = s
		}
	}
	lowest := func(frac float64) SweepStep {
		pick := best
		for _, s := range ok {
			if s.GenTPS >= frac*best.GenTPS && s.Watts < pick.Watts {
				pick = s
			}
		}
		return pick
	}
	reason := func(p SweepStep) string {
		if p.Watts == best.Watts {
			return fmt.Sprintf("highest generation speed: %.1f t/s at %.0f W", p.GenTPS, p.Watts)
		}
		return fmt.Sprintf("%.0f W → %.0f W: %+.1f%% generation speed, %+d °C, %+.0f W",
			best.Watts, p.Watts, (p.GenTPS/best.GenTPS-1)*100, p.MaxTempC-best.MaxTempC, p.AvgPowerW-best.AvgPowerW)
	}
	b, e := lowest(0.95), lowest(0.85)
	return map[string]*Pick{
		"max":       {Watts: best.Watts, Reason: reason(best)},
		"balanced":  {Watts: b.Watts, Reason: reason(b)},
		"efficient": {Watts: e.Watts, Reason: reason(e)},
	}
}
