// Package vram estimates per-GPU memory use of a llama.cpp model configuration:
// weights (from GGUF tensor sizes, assigned to devices like llama.cpp does), KV cache
// (only for layers that have one), and an empirical runtime overhead.
//
// Calibrated on 2× RTX 3060 / llama.cpp b11036 (Qwen3.5-9B Q4_K_M, 64K ctx, layer split
// 1,1, with mmproj): estimate within ~3% of nvidia-smi per GPU.
package vram

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"llamira/internal/gguf"
)

const (
	MiB = 1 << 20
	GiB = 1 << 30

	cudaContext   = 256 * MiB // per used GPU: CUDA context + cuBLAS workspace
	defaultUbatch = 512
	swaPadding    = 512 // SWA caches keep window + one ubatch
)

// GPU is a device the model may use.
type GPU struct {
	Name     string // CUDA0
	Total    int64  // bytes
	Reserved int64  // bytes used by other (non llama-server) processes
}

// Model is the model to estimate, as files on disk.
type Model struct {
	Files  []string // GGUF file(s); all shards of a split model
	MMProj string   // optional multimodal projector
}

// Options are the effective llama-server options ([*] merged with the model's section),
// keyed by canonical option name.
type Options map[string]string

// Device is the estimate for one GPU (or "CPU").
type Device struct {
	Name     string `json:"name"`
	Layers   int    `json:"layers"`
	Weights  int64  `json:"weights"`
	MMProj   int64  `json:"mmproj"`
	KV       int64  `json:"kv"`
	Overhead int64  `json:"overhead"`
	Total    int64  `json:"total"`
	Capacity int64  `json:"capacity,omitempty"` // total minus memory used by other processes
	Status   string `json:"status,omitempty"`   // ok, tight, over
}

// Estimate is the result.
type Estimate struct {
	Architecture string   `json:"architecture"`
	Ctx          int64    `json:"ctx"`
	CtxTrain     int64    `json:"ctx_train"`
	Layers       int      `json:"layers"`
	GPULayers    int      `json:"gpu_layers"`
	KVLayers     int      `json:"kv_layers"`
	SWALayers    int      `json:"swa_layers"`
	KVPerToken   int64    `json:"kv_per_token"` // bytes per token over all full-attention layers
	RecurLayers  int      `json:"recurrent_layers"`
	Slots        int64    `json:"slots"`
	Devices      []Device `json:"devices"`
	CPU          Device   `json:"cpu"`
	Status       string   `json:"status"` // ok, tight, over
	Warnings     []string `json:"warnings"`
	Notes        []string `json:"notes"`
}

// bytes per element of KV cache types (block size accounted for)
var kvTypeSize = map[string]float64{
	"f32": 4, "f16": 2, "bf16": 2, "q8_0": 34.0 / 32, "q4_0": 18.0 / 32, "q4_1": 20.0 / 32,
	"iq4_nl": 18.0 / 32, "q5_0": 22.0 / 32, "q5_1": 24.0 / 32,
}

// swaPatterns: every Nth layer is global (full attention), the others use a sliding window.
// llama.cpp hard-codes these per architecture.
var swaPatterns = map[string]int{"gemma2": 2, "gemma3": 6, "gemma3n": 5, "cohere2": 4, "gpt-oss": 2}

func (o Options) get(k string) string { return strings.TrimSpace(o[k]) }

func (o Options) flag(k string) bool {
	switch strings.ToLower(o.get(k)) {
	case "true", "1", "on":
		return true
	}
	return false
}

func (o Options) int(k string, def int64) int64 {
	if v, err := strconv.ParseInt(o.get(k), 10, 64); err == nil {
		return v
	}
	return def
}

// Run computes the estimate for model files on disk.
func Run(m Model, opts Options, gpus []GPU) (*Estimate, error) {
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
	return Compute(info, tensors, mmproj, opts, gpus)
}

// Compute estimates from parsed GGUF metadata and tensor sizes (local or fetched remotely);
// mmproj is the size of the multimodal projector in bytes (0 = none).
func Compute(info *gguf.Info, tensors []gguf.Tensor, mmproj int64, opts Options, gpus []GPU) (*Estimate, error) {
	e := &Estimate{Architecture: info.Architecture, CtxTrain: int64(info.ContextLength), Layers: int(info.BlockCount),
		Warnings: []string{}, Notes: []string{}}
	nLayer := int(info.BlockCount)

	// ---- tensor sizes per layer -------------------------------------------------------
	layer := make([]int64, nLayer)
	expert := make([]int64, nLayer) // MoE expert tensors (movable to CPU with cpu-moe)
	var output, tokEmbd int64
	hasOutput := false
	for _, t := range tensors {
		switch {
		case strings.HasPrefix(t.Name, "blk."):
			rest := t.Name[4:]
			n, _ := strconv.Atoi(rest[:strings.IndexByte(rest+".", '.')])
			if n >= 0 && n < nLayer {
				if strings.Contains(t.Name, "_exps") {
					expert[n] += t.Size
				} else {
					layer[n] += t.Size
				}
			}
		case strings.HasPrefix(t.Name, "token_embd"):
			tokEmbd += t.Size
		case strings.HasPrefix(t.Name, "output"):
			output += t.Size
			if strings.HasPrefix(t.Name, "output.weight") {
				hasOutput = true
			}
		}
	}
	if !hasOutput {
		output += tokEmbd // tied embeddings: the output head reuses token_embd on the GPU
	}

	// ---- devices -----------------------------------------------------------------------
	devNames := splitList(opts.get("device"))
	var devs []GPU
	if len(devNames) == 0 || (len(devNames) == 1 && strings.EqualFold(devNames[0], "auto")) {
		devs = gpus
	} else if !(len(devNames) == 1 && strings.EqualFold(devNames[0], "none")) {
		for _, n := range devNames {
			i := slices.IndexFunc(gpus, func(g GPU) bool { return strings.EqualFold(g.Name, n) })
			if i < 0 {
				e.Warnings = append(e.Warnings, fmt.Sprintf("device %s not found", n))
				continue
			}
			devs = append(devs, gpus[i])
		}
	}
	mainGPU := int(opts.int("main-gpu", 0))
	if mainGPU < 0 || mainGPU >= len(devs) {
		mainGPU = 0
	}
	splitMode := strings.ToLower(opts.get("split-mode"))
	if splitMode == "" {
		splitMode = "layer"
	}
	if splitMode == "none" && len(devs) > 1 {
		devs = []GPU{devs[mainGPU]}
		mainGPU = 0
	}

	// ---- layer → device assignment (llama.cpp llama_model::load_tensors) ------------------
	ngl := int64(-1) // -1 = all
	switch v := strings.ToLower(opts.get("n-gpu-layers")); v {
	case "", "auto", "all":
		if v == "auto" || v == "" {
			e.Notes = append(e.Notes, "GPU layers: auto (llama.cpp --fit decides; estimated as all layers on GPU)")
		}
	default:
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			ngl = n
		}
	}
	if len(devs) == 0 {
		ngl = 0
	}
	if ngl < 0 || ngl > int64(nLayer)+1 {
		ngl = int64(nLayer) + 1
	}
	iGPUStart := max(nLayer-int(ngl), 0)
	actGPULayers := min(int(ngl), nLayer+1)
	splits := cumulativeSplit(opts.get("tensor-split"), devs)
	deviceOf := func(il int) int { // -1 = CPU
		if len(devs) == 0 || il < iGPUStart || il-iGPUStart >= actGPULayers {
			return -1
		}
		if splitMode == "row" {
			return mainGPU
		}
		f := float64(il-iGPUStart) / float64(actGPULayers)
		d := sort.Search(len(splits), func(i int) bool { return splits[i] > f })
		return min(d, len(devs)-1)
	}
	out := make([]Device, len(devs))
	for i, g := range devs {
		out[i] = Device{Name: g.Name, Capacity: g.Total - g.Reserved}
	}
	cpu := Device{Name: "CPU", Weights: tokEmbd}
	if !hasOutput {
		cpu.Weights = 0
	}
	place := func(d int, w int64) {
		if d < 0 {
			cpu.Weights += w
		} else {
			out[d].Weights += w
		}
	}
	nCPUMoE := opts.int("n-cpu-moe", 0)
	if opts.flag("cpu-moe") {
		nCPUMoE = int64(nLayer)
	}
	for il := 0; il < nLayer; il++ {
		d := deviceOf(il)
		if splitMode == "row" && d >= 0 && len(devs) > 1 {
			for i := range out { // matrix rows split across GPUs
				out[i].Weights += int64(float64(layer[il]) * share(splits, i))
			}
		} else {
			place(d, layer[il])
		}
		if d >= 0 {
			out[d].Layers++
			e.GPULayers++
		}
		if int64(il) < nCPUMoE {
			cpu.Weights += expert[il]
		} else {
			place(d, expert[il])
		}
	}
	place(deviceOf(nLayer), output) // the output layer counts as layer n_layer

	// ---- KV cache ------------------------------------------------------------------------
	ctx := opts.int("ctx-size", 0)
	if ctx <= 0 {
		ctx = int64(info.ContextLength)
		e.Notes = append(e.Notes, fmt.Sprintf("context not set: the model's trained %d tokens is used (--fit may lower it to fit)", ctx))
	}
	e.Ctx = ctx
	headDimK := int64(info.KeyLength)
	headDimV := int64(info.ValueLength)
	if headDimK == 0 && info.HeadCount > 0 {
		headDimK = int64(info.EmbeddingLen / info.HeadCount)
	}
	if headDimV == 0 {
		headDimV = headDimK
	}
	tk, tv := strings.ToLower(opts.get("cache-type-k")), strings.ToLower(opts.get("cache-type-v"))
	if tk == "" {
		tk = "f16"
	}
	if tv == "" {
		tv = "f16"
	}
	fa := strings.ToLower(opts.get("flash-attn"))
	if tv != "f16" && tv != "f32" && tv != "bf16" && fa == "off" {
		e.Warnings = append(e.Warnings, fmt.Sprintf("a quantized V cache (%s) requires flash attention; flash-attn is off", tv))
	}
	swaEvery := swaPatterns[info.Architecture]
	kvOffload := strings.ToLower(opts.get("kv-offload")) != "false"
	slots := opts.int("parallel", -1)
	if slots <= 0 {
		slots = 4 // llama-server "auto"
	}
	e.Slots = slots
	// recurrent state per layer and sequence (llama_hparams::n_embd_r/n_embd_s, f32)
	var recurPerSeq int64
	if info.SSMStateSize > 0 && info.SSMInnerSize > 0 {
		conv := int64(max(info.SSMConvKernel, 1) - 1)
		recurPerSeq = 4 * (conv*int64(info.SSMInnerSize+2*info.SSMGroupCount*info.SSMStateSize) +
			int64(info.SSMStateSize*info.SSMInnerSize))
	}
	for il := 0; il < nLayer; il++ {
		heads := int64(info.HeadCountKV)
		if len(info.HeadCountKVLayers) == nLayer {
			heads = int64(info.HeadCountKVLayers[il])
		} else if info.FullAttnEvery > 0 && (il+1)%int(info.FullAttnEvery) != 0 {
			heads = 0 // hybrid model: recurrent layer, constant-size state only
		}
		if heads == 0 {
			if recurPerSeq > 0 {
				e.RecurLayers++
				d := deviceOf(il)
				if d < 0 || !kvOffload {
					cpu.KV += recurPerSeq * slots
				} else {
					out[d].KV += recurPerSeq * slots
				}
			}
			continue
		}
		// sliding-window layer? (declared per layer by the model, else llama.cpp's per-arch pattern)
		swa := false
		if info.SlidingWindow > 0 {
			if len(info.SWAPattern) == nLayer {
				swa = info.SWAPattern[il] != 0
			} else if swaEvery > 0 {
				swa = (il+1)%swaEvery != 0
			}
		}
		dk, dv := headDimK, headDimV
		if swa && info.KeyLengthSWA > 0 {
			dk = int64(info.KeyLengthSWA)
		}
		if swa && info.ValueLengthSWA > 0 {
			dv = int64(info.ValueLengthSWA)
		}
		perToken := int64(float64(heads*dk)*kvTypeSize[tk] + float64(heads*dv)*kvTypeSize[tv])
		cells := ctx
		if swa && !opts.flag("swa-full") {
			cells = min(ctx, int64(info.SlidingWindow)+swaPadding)
			e.SWALayers++
		} else {
			e.KVLayers++
			e.KVPerToken += perToken
		}
		size := perToken * cells
		d := deviceOf(il)
		if !kvOffload {
			d = -1
		}
		if d < 0 {
			cpu.KV += size
		} else {
			out[d].KV += size
		}
	}
	if info.SlidingWindow > 0 && swaEvery == 0 && len(info.SWAPattern) != nLayer {
		e.Notes = append(e.Notes, "sliding-window model with an unknown layer pattern: KV estimate is an upper bound")
	}

	// ---- multimodal projector -----------------------------------------------------------
	if mmproj > 0 && !opts.flag("no-mmproj") && strings.ToLower(opts.get("mmproj-auto")) != "false" {
		if len(out) > 0 && strings.ToLower(opts.get("mmproj-offload")) != "false" {
			// weights + vision encoder compute buffer (~0.5× weights, measured)
			out[mainGPU].MMProj = mmproj + mmproj/2
		} else {
			cpu.MMProj = mmproj
		}
	}

	// ---- overhead, totals, verdict ---------------------------------------------------------
	ubatch := opts.int("ubatch-size", defaultUbatch)
	compute := ubatch * int64(info.EmbeddingLen) * 48 // activations; ~96 MiB for n_embd 4096
	if fa == "off" {
		// without flash attention the KQ matrix grows with the context
		compute += ubatch * ctx * int64(info.HeadCount) * 4 / int64(max(len(out), 1))
	}
	e.Status = "ok"
	for i := range out {
		if out[i].Layers == 0 && out[i].Weights == 0 && out[i].MMProj == 0 {
			continue
		}
		out[i].Overhead = cudaContext + compute
		out[i].Total = out[i].Weights + out[i].MMProj + out[i].KV + out[i].Overhead
		out[i].Status = verdict(out[i].Total, out[i].Capacity)
		e.Status = worst(e.Status, out[i].Status)
	}
	cpu.Total = cpu.Weights + cpu.KV + cpu.MMProj
	e.Devices, e.CPU = out, cpu

	if e.GPULayers < nLayer && len(devs) > 0 {
		e.Warnings = append(e.Warnings, fmt.Sprintf("only %d of %d layers on GPU: the rest runs on the CPU (much slower)", e.GPULayers, nLayer))
	}
	if info.ContextLength > 0 && ctx > int64(info.ContextLength) {
		e.Warnings = append(e.Warnings, fmt.Sprintf("context %d exceeds the trained %d tokens: quality degrades without RoPE scaling", ctx, info.ContextLength))
	}
	switch e.Status {
	case "over":
		e.Warnings = append(e.Warnings, "estimated not to fit in VRAM: lower the context, quantize the KV cache, use more GPUs, or offload fewer layers")
	case "tight":
		e.Notes = append(e.Notes, "close to the VRAM limit: loading may fail if other processes allocate memory")
	}
	return e, nil
}

func verdict(total, capacity int64) string {
	switch {
	case capacity <= 0:
		return "ok"
	case float64(total) > float64(capacity):
		return "over"
	case float64(total) > 0.92*float64(capacity):
		return "tight"
	}
	return "ok"
}

func worst(a, b string) string {
	rank := map[string]int{"ok": 0, "tight": 1, "over": 2}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// cumulativeSplit returns llama.cpp's normalized cumulative split points. Without an explicit
// tensor-split, llama.cpp splits proportionally to each device's free memory.
func cumulativeSplit(ts string, devs []GPU) []float64 {
	w := make([]float64, len(devs))
	parts := splitList(ts)
	var sum float64
	for i := range devs {
		if len(parts) > 0 {
			if i < len(parts) {
				w[i], _ = strconv.ParseFloat(parts[i], 64)
			}
		} else {
			w[i] = float64(devs[i].Total - devs[i].Reserved)
		}
		sum += w[i]
	}
	out := make([]float64, len(devs))
	var acc float64
	for i := range w {
		if sum > 0 {
			acc += w[i] / sum
		}
		out[i] = acc
	}
	return out
}

func share(splits []float64, i int) float64 {
	if i == 0 {
		return splits[0]
	}
	return splits[i] - splits[i-1]
}
