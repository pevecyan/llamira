package llamacpp

import (
	"os"
	"slices"
	"testing"
)

func loadCatalog(t *testing.T) *Catalog {
	t.Helper()
	b, err := os.ReadFile("testdata/help-b11036.txt")
	if err != nil {
		t.Fatal(err)
	}
	return ParseHelp(string(b))
}

func TestParseHelp(t *testing.T) {
	c := loadCatalog(t)
	if len(c.Args) < 150 {
		t.Fatalf("expected many args, got %d", len(c.Args))
	}
	cases := []struct {
		name, key, env string
		flag           bool
	}{
		{"c", "ctx-size", "LLAMA_ARG_CTX_SIZE", false},
		{"ngl", "n-gpu-layers", "LLAMA_ARG_N_GPU_LAYERS", false},
		{"gpu-layers", "n-gpu-layers", "LLAMA_ARG_N_GPU_LAYERS", false},
		{"hfr", "hf-repo", "LLAMA_ARG_HF_REPO", false},
		{"dev", "device", "LLAMA_ARG_DEVICE", false},
		{"ts", "tensor-split", "LLAMA_ARG_TENSOR_SPLIT", false},
		{"sm", "split-mode", "LLAMA_ARG_SPLIT_MODE", false},
		{"fa", "flash-attn", "LLAMA_ARG_FLASH_ATTN", false},
		{"np", "parallel", "LLAMA_ARG_N_PARALLEL", false},
		{"no-kv-offload", "kv-offload", "LLAMA_ARG_KV_OFFLOAD", true},
		{"nocb", "cont-batching", "", true},
		{"no-host", "no-host", "LLAMA_ARG_NO_HOST", true},
		{"no-mmproj", "mmproj-auto", "", true},
		{"LLAMA_ARG_CTX_SIZE", "ctx-size", "LLAMA_ARG_CTX_SIZE", false},
		{"models-preset", "models-preset", "LLAMA_ARG_MODELS_PRESET", false},
	}
	for _, tc := range cases {
		a, ok := c.Lookup(tc.name)
		if !ok {
			t.Errorf("%s: not found", tc.name)
			continue
		}
		if a.Key != tc.key {
			t.Errorf("%s: key = %q, want %q", tc.name, a.Key, tc.key)
		}
		if tc.env != "" && a.Env != tc.env {
			t.Errorf("%s: env = %q, want %q", tc.name, a.Env, tc.env)
		}
		if a.IsFlag() != tc.flag {
			t.Errorf("%s: flag = %v, want %v (value %q)", tc.name, a.IsFlag(), tc.flag, a.Value)
		}
	}

	ctk, _ := c.Lookup("cache-type-k")
	if !slices.Equal(ctk.Allowed, []string{"f32", "f16", "bf16", "q8_0", "q4_0", "q4_1", "iq4_nl", "q5_0", "q5_1"}) {
		t.Errorf("cache-type-k allowed = %v", ctk.Allowed)
	}
	if c.Known("bogus-key") {
		t.Error("bogus-key should be unknown")
	}
	if !c.Known("load-on-startup") {
		t.Error("load-on-startup is a preset-only key")
	}
}

func TestBuildNumber(t *testing.T) {
	for in, want := range map[string]int{"b11036-911f6cdc8": 11036, "b11517": 11517, "11036 (911f6cdc8)": 11036, "master": 0, "": 0} {
		if got := BuildNumber(in); got != want {
			t.Errorf("BuildNumber(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestParseDevices(t *testing.T) {
	out := `ggml_cuda_init: found 2 CUDA devices:
Available devices:
  CUDA0: NVIDIA GeForce RTX 3060 (12045 MiB, 11891 MiB free)
  CUDA1: NVIDIA GeForce RTX 3060 (12039 MiB, 11885 MiB free)
`
	ds := ParseDevices(out)
	if len(ds) != 2 || ds[0].Name != "CUDA0" || ds[1].Name != "CUDA1" || ds[0].TotalMiB != 12045 || ds[0].Desc != "NVIDIA GeForce RTX 3060" {
		t.Errorf("%+v", ds)
	}
	ds = ParseDevices("Available devices:\n  Vulkan0: AMD Radeon RX 7900 XTX (RADV NAVI31) (24560 MiB, 23000 MiB free)\n")
	if len(ds) != 1 || ds[0].Name != "Vulkan0" || ds[0].Desc != "AMD Radeon RX 7900 XTX (RADV NAVI31)" {
		t.Errorf("%+v", ds)
	}
}
