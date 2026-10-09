// Package models inspects models on disk: the router's Hugging Face cache (LLAMA_CACHE)
// and disk usage of the models directory.
package models

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"llamira/internal/gguf"
	"llamira/internal/hf"
)

// LocalFile is a file of a cached model.
type LocalFile struct {
	Path string `json:"path"` // relative to the snapshot
	Size int64  `json:"size"`
}

// LocalModel is a cached "org/repo:TAG" model as found on disk.
type LocalModel struct {
	ID       string      `json:"id"`
	Repo     string      `json:"repo"`
	Tag      string      `json:"tag"`
	Files    []LocalFile `json:"files"`
	Extras   []string    `json:"extras"` // mmproj / draft files that come with it
	Size     int64       `json:"size"`   // model files + extras
	Modified time.Time   `json:"modified"`
	Info     *gguf.Info  `json:"gguf,omitempty"`
	Error    string      `json:"error,omitempty"`

	Paths      []string `json:"-"` // absolute paths of the model file(s)
	MMProjPath string   `json:"-"` // absolute path of the projector the router would use
}

// Disk is filesystem usage.
type Disk struct {
	Path  string `json:"path"`
	Total uint64 `json:"total"`
	Free  uint64 `json:"free"`
	Used  uint64 `json:"used"`
}

type infoKey struct {
	path  string
	size  int64
	mtime time.Time
}

var (
	infoMu    sync.Mutex
	infoCache = map[infoKey]*gguf.Info{}
)

// ReadInfo reads GGUF metadata, cached by path, size and modification time.
func ReadInfo(path string) (*gguf.Info, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	k := infoKey{path, st.Size(), st.ModTime()}
	infoMu.Lock()
	if i, ok := infoCache[k]; ok {
		infoMu.Unlock()
		return i, nil
	}
	infoMu.Unlock()
	i, err := gguf.ReadFile(path)
	if err != nil {
		return nil, err
	}
	infoMu.Lock()
	infoCache[k] = i
	infoMu.Unlock()
	return i, nil
}

// ScanCache lists the models in a Hugging Face style cache directory
// (models--org--repo/snapshots/<rev>/...), grouped like the router names them.
func ScanCache(cacheDir string) ([]LocalModel, error) {
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		return nil, err
	}
	var out []LocalModel
	for _, e := range entries {
		name, ok := strings.CutPrefix(e.Name(), "models--")
		if !e.IsDir() || !ok {
			continue
		}
		parts := strings.SplitN(name, "--", 2)
		if len(parts) != 2 {
			continue
		}
		repo := parts[0] + "/" + parts[1]
		out = append(out, scanRepo(filepath.Join(cacheDir, e.Name()), repo)...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func scanRepo(dir, repo string) []LocalModel {
	snapDir := filepath.Join(dir, "snapshots")
	var snapshots []string
	if ref, err := os.ReadFile(filepath.Join(dir, "refs", "main")); err == nil {
		snapshots = []string{filepath.Join(snapDir, strings.TrimSpace(string(ref)))}
	}
	if all, err := os.ReadDir(snapDir); err == nil {
		for _, s := range all {
			p := filepath.Join(snapDir, s.Name())
			if len(snapshots) == 0 || p != snapshots[0] {
				snapshots = append(snapshots, p)
			}
		}
	}
	files := map[string]hf.File{}
	abs := map[string]string{}
	mtime := map[string]time.Time{}
	for _, snap := range snapshots {
		filepath.WalkDir(snap, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(snap, p)
			rel = filepath.ToSlash(rel)
			if _, seen := files[rel]; seen {
				return nil
			}
			st, err := os.Stat(p) // follows the symlink into blobs/
			if err != nil {
				return nil // dangling link (e.g. removed blob)
			}
			files[rel] = hf.File{Path: rel, Size: st.Size()}
			abs[rel] = p
			mtime[rel] = st.ModTime()
			return nil
		})
	}
	list := make([]hf.File, 0, len(files))
	for _, f := range files {
		list = append(list, f)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Path < list[j].Path })
	quants, _ := hf.GroupQuants(repo, list)

	var out []LocalModel
	for _, q := range quants {
		m := LocalModel{ID: q.Ref, Repo: repo, Tag: q.Tag, Extras: q.Extras, Size: q.Size + q.ExtraSize}
		for _, x := range q.Extras {
			if strings.Contains(filepath.Base(x), "mmproj") {
				m.MMProjPath = abs[x]
			}
		}
		for _, f := range q.Files {
			m.Paths = append(m.Paths, abs[f.Path])
			m.Files = append(m.Files, LocalFile{f.Path, f.Size})
			if t := mtime[f.Path]; t.After(m.Modified) {
				m.Modified = t
			}
		}
		if len(q.Files) > 0 {
			info, err := ReadInfo(abs[q.Files[0].Path])
			if err != nil {
				m.Error = err.Error()
			} else {
				m.Info = info
			}
		}
		out = append(out, m)
	}
	return out
}

// DirFile is a GGUF model file in the models directory (outside the download cache).
type DirFile struct {
	Path string     `json:"path"`
	Size int64      `json:"size"`
	Info *gguf.Info `json:"gguf,omitempty"`
}

// ScanDir lists model GGUF files under dir (3 levels deep), skipping the cache directory.
func ScanDir(dir, skip string) []DirFile {
	out := []DirFile{}
	if dir == "" {
		return out
	}
	base := strings.Count(filepath.Clean(dir), string(filepath.Separator))
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p == skip || strings.Count(p, string(filepath.Separator))-base >= 3 {
				return filepath.SkipDir
			}
			return nil
		}
		if !gguf.IsModelFile(p) {
			return nil
		}
		if si, ok := gguf.ParseSplit(filepath.Base(p)); ok && si.Count > 1 && si.Index != 1 {
			return nil // list split models once
		}
		st, err := os.Stat(p)
		if err != nil {
			return nil
		}
		f := DirFile{Path: p, Size: st.Size()}
		if info, err := ReadInfo(p); err == nil {
			f.Info = info
		}
		out = append(out, f)
		return nil
	})
	return out
}
