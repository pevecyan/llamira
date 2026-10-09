package models

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScanCache(t *testing.T) {
	cache := t.TempDir()
	repo := filepath.Join(cache, "models--org--Model-GGUF")
	snap := filepath.Join(repo, "snapshots", "abc")
	blobs := filepath.Join(repo, "blobs")
	for _, d := range []string{snap, blobs, filepath.Join(repo, "refs")} {
		os.MkdirAll(d, 0o755)
	}
	os.WriteFile(filepath.Join(repo, "refs", "main"), []byte("abc"), 0o644)
	blob := func(name, link string, size int) {
		os.WriteFile(filepath.Join(blobs, name), make([]byte, size), 0o644)
		os.Symlink(filepath.Join("..", "..", "blobs", name), filepath.Join(snap, link))
	}
	blob("b1", "Model-Q4_K_M.gguf", 100) // not a real GGUF: metadata error is reported, not fatal
	blob("b2", "mmproj-F16.gguf", 10)

	got, err := ScanCache(cache)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %+v", got)
	}
	m := got[0]
	if m.ID != "org/Model-GGUF:Q4_K_M" || m.Size != 110 || len(m.Extras) != 1 || m.Error == "" {
		t.Errorf("got %+v", m)
	}
}
