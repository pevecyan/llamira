package vram

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// writeGGUF writes a minimal GGUF v3 file: metadata kvs (uint32 values or strings) and
// tensors of the given sizes (data is zero-filled).
func writeGGUF(t *testing.T, kvs map[string]any, tensors []struct {
	name string
	size int
}) string {
	t.Helper()
	var b bytes.Buffer
	w := func(v any) { binary.Write(&b, binary.LittleEndian, v) }
	str := func(s string) { w(uint64(len(s))); b.WriteString(s) }
	w(uint32(0x46554747))
	w(uint32(3))
	w(uint64(len(tensors)))
	w(uint64(len(kvs)))
	for k, v := range kvs {
		str(k)
		switch x := v.(type) {
		case string:
			w(uint32(8))
			str(x)
		case int:
			w(uint32(4))
			w(uint32(x))
		}
	}
	var off uint64
	for _, tn := range tensors {
		str(tn.name)
		w(uint32(1))
		w(uint64(tn.size))
		w(uint32(0))
		w(off)
		off += uint64(tn.size)
	}
	for b.Len()%32 != 0 {
		b.WriteByte(0)
	}
	b.Write(make([]byte, off))
	p := filepath.Join(t.TempDir(), "m.gguf")
	if err := os.WriteFile(p, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func hybridModel(t *testing.T) string {
	kvs := map[string]any{
		"general.architecture":           "qwen35",
		"qwen35.block_count":             32,
		"qwen35.context_length":          262144,
		"qwen35.embedding_length":        4096,
		"qwen35.attention.head_count":    16,
		"qwen35.attention.head_count_kv": 4,
		"qwen35.attention.key_length":    256,
		"qwen35.attention.value_length":  256,
		"qwen35.full_attention_interval": 4,
	}
	var ts []struct {
		name string
		size int
	}
	for i := 0; i < 32; i++ {
		ts = append(ts, struct {
			name string
			size int
		}{fmt.Sprintf("blk.%d.ffn_up.weight", i), 1000})
	}
	ts = append(ts, struct {
		name string
		size int
	}{"output.weight", 5000}, struct {
		name string
		size int
	}{"token_embd.weight", 3000})
	return writeGGUF(t, kvs, ts)
}

var twoGPUs = []GPU{{Name: "CUDA0", Total: 12 * GiB}, {Name: "CUDA1", Total: 12 * GiB}}

func TestLayerSplitLikeLlamaCpp(t *testing.T) {
	e, err := Run(Model{Files: []string{hybridModel(t)}},
		Options{"n-gpu-layers": "999", "tensor-split": "1,1", "ctx-size": "65536"}, twoGPUs)
	if err != nil {
		t.Fatal(err)
	}
	// 32 layers + output = 33 slots split 50/50: layers 0..16 on CUDA0, 17..31 + output on CUDA1
	d0, d1 := e.Devices[0], e.Devices[1]
	if d0.Layers != 17 || d1.Layers != 15 || d0.Weights != 17000 || d1.Weights != 15000+5000 {
		t.Errorf("split: %+v / %+v", d0, d1)
	}
	if e.CPU.Weights != 3000 {
		t.Errorf("token_embd stays on CPU: %+v", e.CPU)
	}
	// hybrid: KV only on every 4th layer → 8 layers × 4 heads × 256 × 2 (K+V) × f16
	if e.KVLayers != 8 || e.KVPerToken != 8*4*256*2*2 {
		t.Errorf("kv layers=%d per token=%d", e.KVLayers, e.KVPerToken)
	}
	// layers 3,7,11,15 on CUDA0 and 19,23,27,31 on CUDA1 → equal KV
	if d0.KV != d1.KV || d0.KV != 4*4*256*2*2*65536 {
		t.Errorf("kv: %d / %d", d0.KV, d1.KV)
	}
	if e.Status != "ok" {
		t.Errorf("status %s", e.Status)
	}
}

func TestQuantizedKVAndSingleDevice(t *testing.T) {
	e, err := Run(Model{Files: []string{hybridModel(t)}},
		Options{"device": "CUDA1", "ctx-size": "262144", "cache-type-k": "q8_0", "cache-type-v": "q8_0", "flash-attn": "off"}, twoGPUs)
	if err != nil {
		t.Fatal(err)
	}
	if len(e.Devices) != 1 || e.Devices[0].Name != "CUDA1" || e.Devices[0].Layers != 32 {
		t.Errorf("devices: %+v", e.Devices)
	}
	if e.KVPerToken != 8*4*256*2*34/32 {
		t.Errorf("q8_0 per token = %d", e.KVPerToken)
	}
	found := false
	for _, w := range e.Warnings {
		if bytes.Contains([]byte(w), []byte("requires flash attention")) {
			found = true
		}
	}
	if !found {
		t.Errorf("expected flash-attn warning, got %v", e.Warnings)
	}
}

func TestPartialOffload(t *testing.T) {
	e, err := Run(Model{Files: []string{hybridModel(t)}}, Options{"n-gpu-layers": "16", "ctx-size": "8192"}, twoGPUs)
	if err != nil {
		t.Fatal(err)
	}
	if e.GPULayers != 16 || e.CPU.Weights != 16*1000+5000+3000 {
		t.Errorf("gpu layers %d, cpu %+v", e.GPULayers, e.CPU)
	}
	if len(e.Warnings) == 0 {
		t.Error("expected CPU offload warning")
	}
}

func TestFitContext(t *testing.T) {
	// small GPUs so the KV cache is the limit: KV per token on one GPU = 8 layers × 4 × 256 × 2 × 2 B = 32 KiB
	small := []GPU{{Name: "CUDA0", Total: 2 * GiB}}
	m := Model{Files: []string{hybridModel(t)}}
	r, err := Fit(m, Options{"n-gpu-layers": "999"}, small)
	if err != nil {
		t.Fatal(err)
	}
	if r.Ctx == 0 || r.Ctx%FitStep != 0 || r.Capped {
		t.Fatalf("fit %+v", r)
	}
	if r.Estimate == nil || r.Estimate.Status != "ok" {
		t.Errorf("estimate at fit: %+v", r.Estimate)
	}
	over, _ := Run(m, Options{"n-gpu-layers": "999", "ctx-size": fmt.Sprint(r.Ctx + FitStep)}, small)
	if over.Status == "ok" {
		t.Errorf("ctx %d + one step still ok: not the maximum", r.Ctx)
	}
	if r.Alt == nil || r.Alt.CacheType != "q8_0" || r.Alt.Ctx*2 < 3*r.Ctx {
		t.Errorf("q8_0 alternative: %+v", r.Alt)
	}

	// plenty of VRAM: capped at the trained context
	r, err = Fit(m, Options{"n-gpu-layers": "999"}, twoGPUs)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Capped || r.Ctx != 262144 || r.Alt != nil {
		t.Errorf("capped fit %+v", r)
	}
}
