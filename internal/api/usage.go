package api

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"llamira/internal/models"
	"llamira/internal/presets"
)

// usage returns per-model totals and a time series (hourly for ≤2 days, else daily).
func (s *Server) usage(w http.ResponseWriter, r *http.Request) {
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if days <= 0 || days > 90 {
		days = 7
	}
	since := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	bucket := int64(86400)
	if days <= 2 {
		bucket = 3600
	}
	models, err := s.Store.Usage(since)
	if err != nil {
		writeErr(w, 500, "%v", err)
		return
	}
	series, err := s.Store.Buckets(since, bucket)
	if err != nil {
		writeErr(w, 500, "%v", err)
		return
	}
	writeJSON(w, 200, map[string]any{"days": days, "bucket_seconds": bucket, "models": models, "series": series})
}

// CleanupCandidate is a downloaded model with its last use (by its own id or any preset
// section that uses it).
type CleanupCandidate struct {
	ID       string    `json:"id"`
	Size     int64     `json:"size"`
	LastUsed time.Time `json:"last_used,omitzero"` // zero = no request in the stored history
	Presets  []string  `json:"presets"`
	Status   string    `json:"status"`
	Suggest  bool      `json:"suggest"` // idle for at least `days` and not loaded
}

// usageCleanup suggests downloaded models that were not used for `days` (default 30).
func (s *Server) usageCleanup(w http.ResponseWriter, r *http.Request) {
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if days <= 0 || days > 90 {
		days = 30
	}
	ctx := r.Context()
	local, err := models.ScanCache(s.cache(ctx))
	if err != nil {
		writeErr(w, 500, "%v", err)
		return
	}
	usage, err := s.Store.Usage(time.Now().Add(-91 * 24 * time.Hour))
	if err != nil {
		writeErr(w, 500, "%v", err)
		return
	}
	last := map[string]time.Time{}
	for _, u := range usage {
		last[strings.ToLower(u.Model)] = u.LastUsed
	}
	// preset sections that use each model: by name (override) or hf-repo
	users := map[string][]string{}
	if text, _, err := s.Apply.Read(); err == nil {
		canon := func(k string) string { return k } // without a catalogue: long names only
		if cat, err := s.Catalog.Get(ctx); err == nil {
			canon = cat.Canonical
		}
		for _, sec := range presets.Parse(text).Sections {
			if sec.Name == presets.GlobalSection {
				continue
			}
			users[strings.ToLower(sec.Name)] = append(users[strings.ToLower(sec.Name)], sec.Name)
			for _, kv := range sec.Entries() {
				if canon(kv.Key) == "hf-repo" && !strings.EqualFold(kv.Value, sec.Name) {
					users[strings.ToLower(kv.Value)] = append(users[strings.ToLower(kv.Value)], sec.Name)
				}
			}
		}
	}
	status := map[string]string{}
	if list, err := s.Router.Models(ctx, false); err == nil {
		for _, m := range list {
			status[strings.ToLower(m.ID)] = m.Status.Value
		}
	}
	since := s.Store.FirstRequest()
	cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	out := []CleanupCandidate{}
	for _, lm := range local {
		id := strings.ToLower(lm.ID)
		c := CleanupCandidate{ID: lm.ID, Size: lm.Size, Presets: users[id], Status: status[id]}
		if c.Presets == nil {
			c.Presets = []string{}
		}
		for _, name := range append([]string{lm.ID}, c.Presets...) {
			if t := last[strings.ToLower(name)]; t.After(c.LastUsed) {
				c.LastUsed = t
			}
		}
		running := c.Status == "loaded" || c.Status == "loading" || c.Status == "sleeping" || c.Status == "downloading"
		// "unused for N days" needs N days of history (or a last use older than that)
		known := !since.IsZero() && since.Before(cutoff)
		c.Suggest = !running && ((c.LastUsed.IsZero() && known) || (!c.LastUsed.IsZero() && c.LastUsed.Before(cutoff)))
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Suggest != out[j].Suggest {
			return out[i].Suggest
		}
		return out[i].LastUsed.Before(out[j].LastUsed)
	})
	writeJSON(w, 200, map[string]any{"days": days, "history_since": since, "models": out})
}
