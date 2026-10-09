package vram

import (
	"fmt"
	"maps"
	"strconv"
	"strings"

	"llamira/internal/gguf"
)

// FitStep is the context granularity of Fit.
const FitStep = 1024

// FitResult is the largest context that is estimated to fit (status "ok", not "tight").
type FitResult struct {
	Ctx      int64     `json:"ctx"` // 0 = not even FitStep fits
	Capped   bool      `json:"capped"`
	Estimate *Estimate `json:"estimate,omitempty"`
	// Alt is the same search with a q8_0 KV cache, set when it buys at least 1.5× the context
	// (q8_0 needs 34/32 bytes per element instead of 2, so close to 2× at best).
	Alt *FitAlt `json:"alt,omitempty"`
}

type FitAlt struct {
	CacheType string `json:"cache_type"`
	Ctx       int64  `json:"ctx"`
}

// Fit searches the largest ctx-size (a multiple of FitStep, at most the trained context) for
// which the estimate is "ok" with the other options unchanged.
func Fit(m Model, opts Options, gpus []GPU) (*FitResult, error) {
	if len(m.Files) == 0 {
		return nil, fmt.Errorf("no model file")
	}
	var info *gguf.Info
	var tensors []gguf.Tensor
	for i, f := range m.Files {
		inf, ts, err := gguf.ReadTensors(f)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		if i == 0 {
			info = inf
		}
		tensors = append(tensors, ts...)
	}
	var mmproj int64
	if m.MMProj != "" {
		if _, pts, err := gguf.ReadTensors(m.MMProj); err == nil {
			for _, t := range pts {
				mmproj += t.Size
			}
		}
	}
	return FitParsed(info, tensors, mmproj, opts, gpus)
}

// FitParsed is Fit on already parsed GGUF data.
func FitParsed(info *gguf.Info, tensors []gguf.Tensor, mmproj int64, opts Options, gpus []GPU) (*FitResult, error) {
	limit := int64(info.ContextLength)
	if limit <= 0 {
		limit = 1 << 20
	}
	limit = limit / FitStep * FitStep
	res, err := search(info, tensors, mmproj, opts, gpus, limit)
	if err != nil {
		return nil, err
	}
	quantized := func(k string) bool {
		t := strings.ToLower(opts.get(k))
		return t != "" && t != "f16" && t != "f32" && t != "bf16"
	}
	if !quantized("cache-type-k") && !quantized("cache-type-v") && strings.ToLower(opts.get("flash-attn")) != "off" && !res.Capped {
		q := maps.Clone(opts)
		q["cache-type-k"], q["cache-type-v"] = "q8_0", "q8_0"
		if alt, err := search(info, tensors, mmproj, q, gpus, limit); err == nil && alt.Ctx*2 >= 3*max(res.Ctx, FitStep) {
			res.Alt = &FitAlt{CacheType: "q8_0", Ctx: alt.Ctx}
		}
	}
	return res, nil
}

func search(info *gguf.Info, tensors []gguf.Tensor, mmproj int64, opts Options, gpus []GPU, limit int64) (*FitResult, error) {
	try := func(ctx int64) (*Estimate, error) {
		o := maps.Clone(opts)
		o["ctx-size"] = strconv.FormatInt(ctx, 10)
		return Compute(info, tensors, mmproj, o, gpus)
	}
	if e, err := try(limit); err != nil {
		return nil, err
	} else if e.Status == "ok" {
		return &FitResult{Ctx: limit, Capped: true, Estimate: e}, nil
	}
	lo, hi := int64(0), limit/FitStep // lo fits (0 = nothing), hi does not
	var best *Estimate
	for hi-lo > 1 {
		mid := (lo + hi) / 2
		e, err := try(mid * FitStep)
		if err != nil {
			return nil, err
		}
		if e.Status == "ok" {
			lo, best = mid, e
		} else {
			hi = mid
		}
	}
	return &FitResult{Ctx: lo * FitStep, Estimate: best}, nil
}
