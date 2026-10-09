package api

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"llamira/internal/presets"
)

// presetIntent is a preset section to add once a download finishes ("Download + preset").
type presetIntent struct {
	Model   string    `json:"model"`
	Ctx     int       `json:"ctx,omitempty"`
	Device  string    `json:"device,omitempty"`
	Embed   bool      `json:"embeddings,omitempty"` // embedding model: serve /v1/embeddings
	Started time.Time `json:"started"`
	seen    bool      // the router has listed the model (downloading) at least once
}

type autoPresets struct {
	mu      sync.Mutex
	pending map[string]*presetIntent // by lower-case model ref
	running bool
}

// queueAutoPreset remembers the intent and starts the watcher if needed.
func (s *Server) queueAutoPreset(in presetIntent) {
	a := &s.auto
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pending == nil {
		a.pending = map[string]*presetIntent{}
	}
	in.Started = time.Now()
	a.pending[strings.ToLower(in.Model)] = &in
	if !a.running {
		a.running = true
		go s.watchAutoPresets()
	}
}

// pendingAutoPresets lists models that get a preset when their download finishes.
func (s *Server) pendingAutoPresets() []string {
	s.auto.mu.Lock()
	defer s.auto.mu.Unlock()
	out := []string{}
	for _, in := range s.auto.pending {
		out = append(out, in.Model)
	}
	return out
}

// watchAutoPresets polls the router until every queued download has finished (preset added)
// or failed/disappeared (intent dropped).
func (s *Server) watchAutoPresets() {
	for {
		time.Sleep(5 * time.Second)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		list, err := s.Router.Models(ctx, false)
		cancel()
		var done []*presetIntent
		s.auto.mu.Lock()
		if err == nil {
			for key, in := range s.auto.pending {
				var status string
				var failed bool
				for _, m := range list {
					if strings.EqualFold(m.ID, in.Model) {
						status, failed = m.Status.Value, m.Status.Failed
					}
				}
				switch {
				case status == "" && (in.seen || time.Since(in.Started) > 2*time.Minute):
					s.Log.Warn("auto preset dropped: download gone", "model", in.Model)
					delete(s.auto.pending, key)
				case status == "":
				case status == "downloading":
					in.seen = true
				case failed:
					s.Log.Warn("auto preset dropped: model failed", "model", in.Model)
					delete(s.auto.pending, key)
				default:
					done = append(done, in)
					delete(s.auto.pending, key)
				}
			}
		}
		for key, in := range s.auto.pending {
			if time.Since(in.Started) > 48*time.Hour {
				delete(s.auto.pending, key)
			}
		}
		s.auto.mu.Unlock()

		for _, in := range done {
			if err := s.addPresetFor(in); err != nil {
				s.Log.Error("auto preset", "model", in.Model, "err", err)
			}
		}
		s.auto.mu.Lock()
		if len(s.auto.pending) == 0 {
			s.auto.running = false
			s.auto.mu.Unlock()
			return
		}
		s.auto.mu.Unlock()
	}
}

// presetName derives a section name like the UI does: "unsloth/Qwen3.5-9B-GGUF:Q4_K_M" → "qwen3.5-9b-q4_k_m".
func presetName(ref string, taken func(string) bool) string {
	repo, tag, _ := strings.Cut(ref, ":")
	base := repo[strings.LastIndex(repo, "/")+1:]
	if len(base) > 5 && strings.EqualFold(base[len(base)-5:], "-gguf") {
		base = base[:len(base)-5]
	}
	base = strings.ToLower(base)
	if tag != "" {
		base += "-" + strings.ToLower(tag)
	}
	n := base
	for i := 2; taken(n); i++ {
		n = fmt.Sprintf("%s-%d", base, i)
	}
	return n
}

// addPresetFor adds a preset section for a finished download through the normal apply
// pipeline (validation, dry run, backup, reload, rollback). It does nothing if a preset
// already references the model.
func (s *Server) addPresetFor(in *presetIntent) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if refs, err := s.presetRefs(ctx, in.Model); err != nil {
		return err
	} else if len(refs) > 0 {
		s.Log.Info("auto preset skipped: already referenced", "model", in.Model, "sections", refs)
		return nil
	}
	text, hash, err := s.Apply.Read()
	if err != nil {
		return err
	}
	cat, err := s.Catalog.Get(ctx)
	if err != nil {
		return err
	}
	d := presets.Parse(text)
	name := presetName(in.Model, func(n string) bool { return d.Section(n) != nil })
	kvs := []presets.KV{{Key: "hf-repo", Value: in.Model}}
	if in.Ctx > 0 {
		kvs = append(kvs, presets.KV{Key: "ctx-size", Value: strconv.Itoa(in.Ctx)})
	}
	if in.Device != "" {
		kvs = append(kvs, presets.KV{Key: "device", Value: in.Device})
	}
	if in.Embed {
		kvs = append(kvs, presets.KV{Key: "embeddings", Value: "true"})
	}
	d.Upsert(name, kvs, cat.Canonical)
	res, err := s.Apply.Apply(ctx, d.String(), hash, fmt.Sprintf("add preset [%s] after download", name))
	if err != nil {
		return err
	}
	if !res.OK {
		return fmt.Errorf("apply failed: %s", res.Error)
	}
	s.Log.Info("auto preset added", "model", in.Model, "section", name, "apply", res.ApplyID)
	return nil
}
