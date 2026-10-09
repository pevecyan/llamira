package gguf

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestParseSplit(t *testing.T) {
	cases := []struct {
		in, prefix, tag string
		idx, count      int
	}{
		{"Qwen3.5-9B-Q5_K_M.gguf", "Qwen3.5-9B-Q5_K_M", "Q5_K_M", 1, 1},
		{"sub/Kimi-K2-UD-IQ1_S-00002-of-00006.gguf", "sub/Kimi-K2-UD-IQ1_S", "IQ1_S", 2, 6},
		{"model.q8_0.gguf", "model.q8_0", "Q8_0", 1, 1},
		{"Flux2-Klein-9B-True-v2-Q8_0.gguf", "Flux2-Klein-9B-True-v2-Q8_0", "Q8_0", 1, 1},
		{"model-bf16.gguf", "model-bf16", "BF16", 1, 1},
	}
	for _, c := range cases {
		si, ok := ParseSplit(c.in)
		if !ok || si.Prefix != c.prefix || si.Tag != c.tag || si.Index != c.idx || si.Count != c.count {
			t.Errorf("%s: got %+v", c.in, si)
		}
	}
	if _, ok := ParseSplit("README.md"); ok {
		t.Error("README.md is not gguf")
	}
}

func TestIsModelFile(t *testing.T) {
	for path, want := range map[string]bool{
		"Qwen-Q4_K_M.gguf": true, "mmproj-F16.gguf": false, "x/mmproj-BF16.gguf": false,
		"imatrix.gguf": false, "mtp-Q8_0.gguf": false, "a.safetensors": false,
	} {
		if IsModelFile(path) != want {
			t.Errorf("%s: want %v", path, want)
		}
	}
}

func TestRead(t *testing.T) {
	var b bytes.Buffer
	w := func(v any) { binary.Write(&b, binary.LittleEndian, v) }
	str := func(s string) { w(uint64(len(s))); b.WriteString(s) }
	w(uint32(0x46554747))
	w(uint32(3))
	w(uint64(0)) // tensors
	w(uint64(6)) // kvs
	str("general.architecture")
	w(uint32(tString))
	str("qwen35")
	str("general.file_type")
	w(uint32(tUint32))
	w(uint32(17))
	str("qwen35.context_length")
	w(uint32(tUint32))
	w(uint32(262144))
	str("tokenizer.ggml.tokens") // string array must be skipped
	w(uint32(tArray))
	w(uint32(tString))
	w(uint64(2))
	str("a")
	str("bb")
	str("qwen35.attention.head_count_kv") // per-layer array → max
	w(uint32(tArray))
	w(uint32(tUint32))
	w(uint64(3))
	w(uint32(0))
	w(uint32(4))
	w(uint32(0))
	str("qwen35.block_count")
	w(uint32(tUint64))
	w(uint64(32))

	info, err := Read(&b)
	if err != nil {
		t.Fatal(err)
	}
	if info.Architecture != "qwen35" || info.FileType != "Q5_K_M" || info.ContextLength != 262144 ||
		info.HeadCountKV != 4 || info.BlockCount != 32 {
		t.Errorf("got %+v", info)
	}
}
