package hf

import "testing"

func TestGroupQuants(t *testing.T) {
	files := []File{
		{Path: ".gitattributes", Size: 1},
		{Path: "Qwen3.5-9B-BF16.gguf", Size: 18},
		{Path: "Qwen3.5-9B-Q4_K_M.gguf", Size: 5},
		{Path: "Qwen3.5-9B-UD-Q4_K_XL.gguf", Size: 6},
		{Path: "mmproj-BF16.gguf", Size: 1},
		{Path: "mmproj-F16.gguf", Size: 1},
		{Path: "imatrix_unsloth.gguf", Size: 1},
		{Path: "Q8_0/Big-Q8_0-00001-of-00002.gguf", Size: 10},
		{Path: "Q8_0/Big-Q8_0-00002-of-00002.gguf", Size: 10},
		{Path: "Big-Q8_0.gguf", Size: 3}, // same tag as the split group, listed later
	}
	quants, sidecars := GroupQuants("org/repo", files)
	if len(sidecars) != 2 {
		t.Errorf("sidecars = %v", sidecars)
	}
	by := map[string][]Quant{}
	for _, q := range quants {
		by[q.Tag] = append(by[q.Tag], q)
	}
	if q := by["Q4_K_M"]; len(q) != 1 || q[0].Ref != "org/repo:Q4_K_M" || q[0].Size != 5 || !q[0].Selectable {
		t.Errorf("Q4_K_M = %+v", q)
	}
	if q := by["Q4_K_XL"]; len(q) != 1 || q[0].Bits != 4 {
		t.Errorf("Q4_K_XL = %+v", q)
	}
	if q := by["BF16"]; len(q) != 1 || len(q[0].Extras) != 1 || q[0].Extras[0] != "mmproj-BF16.gguf" {
		t.Errorf("BF16 extras = %+v", q)
	}
	q8 := by["Q8_0"]
	if len(q8) != 2 {
		t.Fatalf("Q8_0 groups = %+v", q8)
	}
	for _, q := range q8 {
		if !q.Ambiguous {
			t.Error("Q8_0 should be ambiguous")
		}
		split := len(q.Files) == 2
		if split && (q.Size != 20 || !q.Selectable) {
			t.Errorf("split Q8_0 should be the selectable one: %+v", q)
		}
		if !split && q.Selectable {
			t.Errorf("single Q8_0 is shadowed: %+v", q)
		}
	}
}
