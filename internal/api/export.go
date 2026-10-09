package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"slices"
	"sort"
	"strconv"
	"time"

	"llamira/internal/alerts"
	"llamira/internal/config"
	"llamira/internal/install"
	"llamira/internal/schedule"
	"llamira/internal/system"
)

// Bundle is a host export: everything needed to rebuild a host's setup elsewhere (models
// themselves are not included; presets refer to them by id).
type Bundle struct {
	Format      int              `json:"llamira_export"` // 1
	Version     string           `json:"version"`
	Host        string           `json:"host"`
	Time        time.Time        `json:"time"`
	Secrets     bool             `json:"secrets"` // tokens included
	Presets     string           `json:"presets"`
	Schedules   []schedule.Rule  `json:"schedules"`
	Alerts      *alerts.Settings `json:"alerts,omitempty"`
	PowerLimits map[int]float64  `json:"power_limits,omitempty"`
	Config      *config.Server   `json:"config,omitempty"` // for reference; not imported (root-owned file)
}

// Redacted marks a secret left out of an export.
const Redacted = "(redacted)"

func redact(s *string) {
	if *s != "" {
		*s = Redacted
	}
}

func (s *Server) export(w http.ResponseWriter, r *http.Request) {
	secrets := r.URL.Query().Get("secrets") != ""
	text, _, err := s.Apply.Read()
	if err != nil {
		writeErr(w, 500, "%v", err)
		return
	}
	host, _ := os.Hostname()
	b := Bundle{Format: 1, Version: s.Version, Host: host, Time: time.Now().UTC(), Secrets: secrets, Presets: text}
	b.Schedules, _ = s.Store.Schedules()
	for i := range b.Schedules {
		b.Schedules[i].LastRun, b.Schedules[i].LastResult = time.Time{}, ""
	}
	al := s.alertSettings()
	cfg := s.Cfg
	if !secrets {
		redact(&al.TelegramToken)
		redact(&al.NtfyToken)
		for _, p := range []*string{&cfg.AuthToken, &cfg.ReadToken, &cfg.PasswordHash, &cfg.HFToken, &cfg.Router.APIKey} {
			redact(p)
		}
	}
	b.Alerts, b.Config = &al, &cfg
	if pl := install.LoadPowerLimits(); len(pl) > 0 {
		b.PowerLimits = pl
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="llamira-%s-%s.json"`, host, time.Now().Format("2006-01-02")))
	writeJSON(w, 200, b)
}

// importBundle imports schedules, alert settings and power limits from a bundle. Presets are
// not written here: the client loads them into the working copy, and they go live through the
// normal review & apply. Redacted secrets keep this host's current values.
func (s *Server) importBundle(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Bundle struct {
			Bundle
			Alerts json.RawMessage `json:"alerts"` // decoded onto the current settings below
		} `json:"bundle"`
		Parts []string `json:"parts"` // schedules, alerts, power
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "%v", err)
		return
	}
	b := req.Bundle
	if b.Format != 1 {
		writeErr(w, 400, "not a llamira export (llamira_export = %d)", b.Format)
		return
	}
	var done []string
	var errs []string
	if slices.Contains(req.Parts, "schedules") {
		for i := range b.Schedules {
			if err := b.Schedules[i].Validate(); err != nil {
				writeErr(w, 400, "schedule %d: %v", i+1, err)
				return
			}
			b.Schedules[i].LastRun, b.Schedules[i].LastResult = time.Time{}, ""
		}
		if b.Schedules == nil {
			b.Schedules = []schedule.Rule{}
		}
		if err := s.Store.SetSchedules(b.Schedules); err != nil {
			errs = append(errs, "schedules: "+err.Error())
		} else {
			done = append(done, fmt.Sprintf("%d schedule(s)", len(b.Schedules)))
		}
	}
	if slices.Contains(req.Parts, "alerts") && len(b.Alerts) > 0 && string(b.Alerts) != "null" {
		// fields missing from the bundle keep this host's values
		cur := s.alertSettings()
		a := cur
		if err := json.Unmarshal(b.Alerts, &a); err != nil {
			writeErr(w, 400, "alerts: %v", err)
			return
		}
		if a.TelegramToken == Redacted {
			a.TelegramToken = cur.TelegramToken
		}
		if a.NtfyToken == Redacted {
			a.NtfyToken = cur.NtfyToken
		}
		if err := s.Store.SetAlertSettings(a); err != nil {
			errs = append(errs, "alerts: "+err.Error())
		} else {
			done = append(done, "alert settings")
		}
	}
	if slices.Contains(req.Parts, "power") && len(b.PowerLimits) > 0 {
		idx := make([]int, 0, len(b.PowerLimits))
		for i := range b.PowerLimits {
			idx = append(idx, i)
		}
		sort.Ints(idx)
		ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
		defer cancel()
		for _, i := range idx {
			spec := strconv.Itoa(i) + ":" + strconv.FormatFloat(b.PowerLimits[i], 'f', 0, 64)
			if _, err := system.Privileged(ctx, install.BinPath, "power", spec); err != nil {
				errs = append(errs, fmt.Sprintf("power gpu%d: %v", i, err))
			} else {
				done = append(done, fmt.Sprintf("gpu%d power limit %.0f W", i, b.PowerLimits[i]))
			}
		}
	}
	s.Log.Info("import", "from", b.Host, "done", done, "errors", errs)
	writeJSON(w, 200, map[string]any{"imported": done, "errors": errs})
}
