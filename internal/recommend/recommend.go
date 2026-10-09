// Package recommend suggests models from a curated catalogue that fit this host: for each entry
// it picks the largest quantization whose VRAM estimate (internal/vram) fits with a margin.
package recommend

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"llamira/internal/gguf"
	"llamira/internal/hf"
	"llamira/internal/vram"
)

//go:embed catalog.json
var embedded []byte

// Entry is one curated model.
type Entry struct {
	Name  string   `json:"name"`
	Use   []string `json:"use"`
	Repo  string   `json:"repo"`
	Ctx   int64    `json:"ctx"`
	Notes string   `json:"notes"`
}

type Catalog struct {
	Version string  `json:"version"`
	Entries []Entry `json:"entries"`
	Source  string  `json:"source"`
}

// Option is one quantization with its estimate.
type Option struct {
	Tag       string   `json:"tag"`
	Ref       string   `json:"ref"`
	Size      int64    `json:"size"` // download size incl. projector
	VRAM      []int64  `json:"vram"` // estimated bytes per used GPU
	Devices   []string `json:"devices"`
	Status    string   `json:"status"`    // ok, tight, over
	Placement string   `json:"placement"` // one-gpu, all-gpus
}

// Result is a recommendation for one catalogue entry.
type Result struct {
	Entry
	Tier         string   `json:"tier"` // one-gpu, all-gpus, offload, unsupported, error
	Pick         *Option  `json:"pick,omitempty"`
	Smaller      *Option  `json:"smaller,omitempty"`
	Larger       *Option  `json:"larger,omitempty"`
	Reason       string   `json:"reason"`
	Architecture string   `json:"architecture,omitempty"`
	Params       int64    `json:"params,omitempty"`
	Supported    bool     `json:"supported"`
	Installed    []string `json:"installed"`
	Error        string   `json:"error,omitempty"`
}

type Engine struct {
	HF         *hf.Client
	CatalogURL string // optional override (refreshed without a release)

	mu      sync.Mutex
	cat     *Catalog
	catTime time.Time
	cache   map[string]*Result // per repo+gpus+ctx
	cacheAt time.Time
}

// Catalog returns the catalogue (remote URL if configured and reachable, else embedded).
func (e *Engine) Catalog(ctx context.Context) *Catalog {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cat != nil && time.Since(e.catTime) < time.Hour {
		return e.cat
	}
	var c Catalog
	_ = json.Unmarshal(embedded, &c)
	c.Source = "built-in"
	if e.CatalogURL != "" {
		if rc, err := fetchCatalog(ctx, e.CatalogURL); err == nil {
			c = *rc
			c.Source = e.CatalogURL
		}
	}
	e.cat, e.catTime = &c, time.Now()
	return e.cat
}

func fetchCatalog(ctx context.Context, url string) (*Catalog, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var c Catalog
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&c); err != nil {
		return nil, err
	}
	if len(c.Entries) == 0 {
		return nil, fmt.Errorf("empty catalogue")
	}
	return &c, nil
}

// Recommend evaluates every catalogue entry for the given GPUs. installed are local model ids
// (org/repo:TAG); supports reports whether the running llama.cpp knows an architecture.
func (e *Engine) Recommend(ctx context.Context, gpus []vram.GPU, installed []string, supports func(string) bool, refresh bool) ([]Result, *Catalog) {
	cat := e.Catalog(ctx)
	e.mu.Lock()
	if e.cache == nil || refresh || time.Since(e.cacheAt) > 6*time.Hour {
		e.cache, e.cacheAt = map[string]*Result{}, time.Now()
	}
	e.mu.Unlock()

	key := func(en Entry) string {
		var g []string
		for _, x := range gpus {
			g = append(g, fmt.Sprintf("%s:%d:%d", x.Name, x.Total>>26, x.Reserved>>26))
		}
		return fmt.Sprintf("%s|%d|%s", en.Repo, en.Ctx, strings.Join(g, ","))
	}
	out := make([]Result, len(cat.Entries))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i, en := range cat.Entries {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			k := key(en)
			e.mu.Lock()
			cached := e.cache[k]
			e.mu.Unlock()
			var r Result
			if cached != nil {
				r = *cached
			} else {
				r = e.evaluate(ctx, en, gpus)
				if r.Error == "" {
					e.mu.Lock()
					e.cache[k] = &r
					e.mu.Unlock()
				}
			}
			// per-request facts (not cached)
			r.Installed = []string{}
			for _, id := range installed {
				if strings.HasPrefix(strings.ToLower(id), strings.ToLower(en.Repo)+":") {
					r.Installed = append(r.Installed, id)
				}
			}
			r.Supported = r.Architecture == "" || supports == nil || supports(r.Architecture)
			if !r.Supported && r.Tier != "error" {
				r.Tier = "unsupported"
				r.Reason = fmt.Sprintf("architecture %q is not supported by the running llama.cpp; update it on the llama.cpp page", r.Architecture)
			}
			out[i] = r
		}()
	}
	wg.Wait()
	return out, cat
}

// Headroom is the share of a GPU's free memory a recommendation may use.
const Headroom = 0.88

// maxBits: recommending above 8-bit wastes memory for no visible gain.
const maxBits = 8

// ladder lists the quantizations recommendations choose from, best quality first. IQ4_XS and
// above are considered good; 3-bit only as a flagged last resort.
var ladder = []string{"Q8_0", "Q6_K", "Q5_K_M", "Q4_K_M", "IQ4_XS", "Q3_K_M", "IQ3_XXS"}

func (e *Engine) evaluate(ctx context.Context, en Entry, gpus []vram.GPU) Result {
	r := Result{Entry: en, Installed: []string{}}
	fail := func(err error) Result {
		r.Tier, r.Error, r.Reason = "error", err.Error(), "could not evaluate: "+err.Error()
		return r
	}
	d, err := e.HF.Repo(ctx, en.Repo)
	if err != nil {
		return fail(err)
	}
	r.Params = d.Params
	var qs []hf.Quant
	for _, q := range d.Quants {
		if q.Selectable && q.Bits > 0 && q.Bits <= maxBits {
			qs = append(qs, q)
		}
	}
	if len(qs) == 0 {
		return fail(fmt.Errorf("no usable GGUF quantizations"))
	}
	sort.Slice(qs, func(i, j int) bool { return qs[i].Size < qs[j].Size })

	// one header (a single-file quant near the middle) gives the architecture and per-layer
	// sizes; other quants are scaled by file size
	ref := qs[len(qs)/2]
	for _, q := range qs {
		if len(q.Files) == 1 && (q.Tag == "Q4_K_M" || q.Tag == "Q4_K_XL") {
			ref = q
			break
		}
	}
	info, tensors, err := e.HF.FetchHeader(ctx, en.Repo, ref.Files[0].Path, ref.Files[0].Size)
	if err != nil {
		return fail(err)
	}
	r.Architecture = info.Architecture
	var refSum int64
	for _, t := range tensors {
		refSum += t.Size
	}
	if len(ref.Files) > 1 || refSum == 0 {
		tensors = uniformTensors(info, ref.Size)
		refSum = ref.Size
	}
	scaled := func(q hf.Quant) []gguf.Tensor {
		f := float64(q.Size) / float64(refSum)
		out := make([]gguf.Tensor, len(tensors))
		for i, t := range tensors {
			out[i] = gguf.Tensor{Name: t.Name, Size: int64(float64(t.Size) * f)}
		}
		return out
	}
	mmproj := func(q hf.Quant) int64 {
		var n int64
		for _, x := range q.Extras {
			if strings.Contains(filepath.Base(x), "mmproj") {
				for _, s := range d.Sidecars {
					if s.Path == x {
						n += s.Size
					}
				}
			}
		}
		return n
	}
	opts := vram.Options{"ctx-size": fmt.Sprint(en.Ctx), "n-gpu-layers": "all"}
	best := func(g []vram.GPU, placement string) []Option {
		var res []Option
		for _, q := range qs {
			est, err := vram.Compute(info, scaled(q), mmproj(q), opts, g)
			if err != nil {
				continue
			}
			o := Option{Tag: q.Tag, Ref: q.Ref, Size: q.Size + q.ExtraSize, Status: "ok", Placement: placement}
			for _, dv := range est.Devices {
				if dv.Total > 0 {
					o.VRAM = append(o.VRAM, dv.Total)
					o.Devices = append(o.Devices, dv.Name)
					// recommendations keep headroom for other GPU users and estimate error
					if dv.Capacity > 0 && float64(dv.Total) > Headroom*float64(dv.Capacity) {
						o.Status = "over"
					}
				}
			}
			res = append(res, o)
		}
		return res
	}
	// the largest single GPU (by free memory) vs. all GPUs
	var single []vram.GPU
	if len(gpus) > 0 {
		g := gpus[0]
		for _, x := range gpus[1:] {
			if x.Total-x.Reserved > g.Total-g.Reserved {
				g = x
			}
		}
		single = []vram.GPU{g}
	}
	oneOpts, allOpts := best(single, "one-gpu"), best(gpus, "all-gpus")

	// quality ladder (best first); repos without these tags fall back to all quants by size
	rank := map[string]int{}
	for i, t := range ladder {
		rank[t] = i
	}
	onLadder := func(list []Option) []Option {
		var out []Option
		for _, o := range list {
			if _, ok := rank[o.Tag]; ok {
				out = append(out, o)
			}
		}
		if len(out) == 0 { // e.g. MXFP4-only or Q8_0/F16-only repos
			out = append(out, list...)
			sort.Slice(out, func(i, j int) bool { return out[i].Size > out[j].Size })
			return out
		}
		sort.Slice(out, func(i, j int) bool { return rank[out[i].Tag] < rank[out[j].Tag] })
		return out
	}
	oneL, allL := onLadder(oneOpts), onLadder(allOpts)
	firstFit := func(list []Option) int { // best quality that fits
		for i, o := range list {
			if o.Status == "ok" {
				return i
			}
		}
		return -1
	}
	good := func(o Option) bool { // at least IQ4_XS quality
		rk, ok := rank[o.Tag]
		return !ok || rk <= rank["IQ4_XS"]
	}
	var list []Option
	idx := -1
	if i := firstFit(oneL); len(single) > 0 && i >= 0 && good(oneL[i]) {
		list, idx, r.Tier = oneL, i, "one-gpu"
	} else if i := firstFit(allL); len(gpus) > 1 && i >= 0 && good(allL[i]) {
		list, idx, r.Tier = allL, i, "all-gpus"
	} else if i := firstFit(allL); i >= 0 {
		list, idx, r.Tier = allL, i, "low-quality"
	} else {
		list, idx, r.Tier = allL, len(allL)-1, "offload"
	}
	if len(list) == 0 || idx < 0 {
		return fail(fmt.Errorf("no estimate"))
	}
	p := list[idx]
	r.Pick = &p
	if idx+1 < len(list) {
		s := list[idx+1] // one step smaller / lower quality
		r.Smaller = &s
	}
	if idx > 0 {
		l := list[idx-1] // one step better
		r.Larger = &l
	}
	r.Reason = reason(p, en.Ctx, gpus, r.Tier)
	return r
}

func reason(o Option, ctx int64, gpus []vram.GPU, tier string) string {
	var tot int64
	for _, v := range o.VRAM {
		tot += v
	}
	gb := func(n int64) string { return fmt.Sprintf("%.1f GB", float64(n)/1e9) }
	where := strings.Join(o.Devices, " + ")
	switch tier {
	case "one-gpu":
		return fmt.Sprintf("%s, ~%s at %dK context, fits on %s", o.Tag, gb(tot), ctx/1024, where)
	case "all-gpus":
		return fmt.Sprintf("%s, ~%s at %dK context, split over %s", o.Tag, gb(tot), ctx/1024, where)
	case "low-quality":
		return fmt.Sprintf("only %s fits (~%s at %dK over %s): 3-bit, noticeably lower quality", o.Tag, gb(tot), ctx/1024, where)
	}
	return fmt.Sprintf("even %s (~%s at %dK) does not fit the GPUs; it would need CPU offload (slow)", o.Tag, gb(tot), ctx/1024)
}

// uniformTensors fakes a per-layer tensor table for split models (only the first shard's
// header is fetched): 90% of the weights spread over the layers, the rest embeddings/output.
func uniformTensors(info *gguf.Info, size int64) []gguf.Tensor {
	n := int(max(info.BlockCount, 1))
	layer := size * 9 / 10 / int64(n)
	ts := []gguf.Tensor{{Name: "token_embd.weight", Size: size / 20}, {Name: "output.weight", Size: size / 20}}
	for i := 0; i < n; i++ {
		ts = append(ts, gguf.Tensor{Name: fmt.Sprintf("blk.%d.ffn.weight", i), Size: layer})
	}
	return ts
}

// ArchSupport reports whether the llama.cpp library next to a llama-server binary knows an
// architecture: llama.cpp keeps its architecture names as C strings in libllama.
type ArchSupport struct {
	mu   sync.Mutex
	path string
	lib  []byte
}

func (a *ArchSupport) For(binary string) func(string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.path != binary {
		a.path, a.lib = binary, nil
		matches, _ := filepath.Glob(filepath.Join(filepath.Dir(binary), "libllama.so*"))
		for _, m := range matches {
			if b, err := os.ReadFile(m); err == nil && len(b) > 0 {
				a.lib = b
				break
			}
		}
	}
	lib := a.lib
	return func(arch string) bool {
		if len(lib) == 0 || arch == "" {
			return true // unknown: don't block
		}
		return bytes.Contains(lib, []byte("\x00"+arch+"\x00"))
	}
}
