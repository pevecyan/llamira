package apply

import (
	"testing"

	"llamira/internal/llamacpp"
)

func model(id, status, preset string) llamacpp.Model {
	return llamacpp.Model{ID: id, Status: llamacpp.ModelStatus{Value: status, Preset: preset}}
}

func TestImpact(t *testing.T) {
	live := []llamacpp.Model{
		model("a", "loaded", "[a]\nctx-size = 1\n"),   // unchanged, stays loaded
		model("b", "loaded", "[b]\nctx-size = 1\n"),   // changed while loaded → unloads
		model("c", "unloaded", "[c]\nctx-size = 1\n"), // changed while unloaded
		model("d", "sleeping", "[d]\n"),               // removed while sleeping → unloads
	}
	dry := []llamacpp.Model{
		model("a", "unloaded", "[a]\nctx-size = 1\n"),
		model("b", "unloaded", "[b]\nctx-size = 2\n"),
		model("c", "unloaded", "[c]\nctx-size = 2\n"),
		model("e", "unloaded", "[e]\n"),
	}
	got := map[string]ModelChange{}
	for _, c := range impact(live, dry) {
		got[c.ID] = c
	}
	if _, ok := got["a"]; ok {
		t.Error("a is unchanged and must not be reported")
	}
	want := map[string]struct {
		change  string
		unloads bool
	}{"b": {"changed", true}, "c": {"changed", false}, "d": {"removed", true}, "e": {"added", false}}
	for id, w := range want {
		c, ok := got[id]
		if !ok || c.Change != w.change || c.Unloads != w.unloads {
			t.Errorf("%s: got %+v, want %+v", id, c, w)
		}
	}
}

func TestCheckModelPath(t *testing.T) {
	dir := t.TempDir()
	m := &Manager{ModelsDir: dir}
	if msg := m.checkModelPath("relative.gguf"); msg == "" {
		t.Error("relative path must be rejected")
	}
	if msg := m.checkModelPath("/etc/passwd"); msg == "" {
		t.Error("path outside models dir must be rejected")
	}
	if msg := m.checkModelPath(dir + "/missing.gguf"); msg == "" {
		t.Error("missing file must be rejected")
	}
}
