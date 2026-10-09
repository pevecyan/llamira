package presets

import (
	"strings"
	"testing"
)

const sample = `version = 1

; Global defaults
[*]
ctx-size = 65536
n-gpu-layers = 999

; my model
[ggml-org/MY-MODEL-GGUF:Q8_0]
# keep this comment
c = 4096
jinja = true
weird line without equals

[custom_model]
model = /models/x.gguf
`

func TestRoundTrip(t *testing.T) {
	for _, s := range []string{sample, "", "[a]\nk = v", "version = 1\n", "\n\n[x]\n\n"} {
		if got := Parse(s).String(); got != s {
			t.Errorf("round trip mismatch:\n--- want\n%q\n--- got\n%q", s, got)
		}
	}
}

func TestParse(t *testing.T) {
	d := Parse(sample)
	if got := strings.Join(d.Names(), ","); got != "*,ggml-org/MY-MODEL-GGUF:Q8_0,custom_model" {
		t.Fatalf("names = %s", got)
	}
	if v, _ := d.Preamble.Get("version"); v != "1" {
		t.Errorf("version = %q", v)
	}
	s := d.Section("ggml-org/MY-MODEL-GGUF:Q8_0")
	if v, _ := s.Get("c"); v != "4096" {
		t.Errorf("c = %q", v)
	}
	if len(s.Entries()) != 2 {
		t.Errorf("entries = %v", s.Entries())
	}
}

func TestSetEntriesPreservesComments(t *testing.T) {
	canon := func(k string) string {
		if k == "c" {
			return "ctx-size"
		}
		return k
	}
	d := Parse(sample)
	d.Section("ggml-org/MY-MODEL-GGUF:Q8_0").SetEntries([]KV{
		{"ctx-size", "8192"}, // replaces "c = 4096" in place
		{"device", "CUDA1"},  // new, appended
	}, canon)
	want := `version = 1

; Global defaults
[*]
ctx-size = 65536
n-gpu-layers = 999

; my model
[ggml-org/MY-MODEL-GGUF:Q8_0]
# keep this comment
ctx-size = 8192
weird line without equals
device = CUDA1

[custom_model]
model = /models/x.gguf
`
	if got := d.String(); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestUpsertAndDelete(t *testing.T) {
	d := Parse("version = 1\n\n[a]\nx = 1\n")
	d.Upsert("b", []KV{{"model", "/m.gguf"}}, nil)
	d.Upsert(GlobalSection, []KV{{"ctx-size", "1024"}}, nil)
	want := "version = 1\n\n[*]\nctx-size = 1024\n\n[a]\nx = 1\n\n[b]\nmodel = /m.gguf\n"
	if got := d.String(); got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
	if !d.Delete("a") || d.Section("a") != nil {
		t.Error("delete failed")
	}
	if !d.Rename("b", "c") || d.Section("c") == nil {
		t.Error("rename failed")
	}
}

func TestUpsertIntoEmpty(t *testing.T) {
	d := Parse("")
	d.SetPreamble("version", "1")
	d.Upsert("m", []KV{{"hf-repo", "a/b:Q4_K_M"}}, nil)
	if got, want := d.String(), "version = 1\n\n[m]\nhf-repo = a/b:Q4_K_M\n"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestDiff(t *testing.T) {
	d := Diff("a\nb\nc\n", "a\nc\nd\n")
	var ops string
	for _, l := range d {
		ops += l.Op
	}
	if ops != " - +" {
		t.Errorf("ops = %q (%v)", ops, d)
	}
	if !Changed(d) || Changed(Diff("x\n", "x\n")) {
		t.Error("Changed wrong")
	}
}
