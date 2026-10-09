package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"llamira/internal/doctor"
	"llamira/internal/gpu"
	"llamira/internal/models"
	"llamira/internal/system"
)

// Doctor gathers the host state and runs the doctor checks.
func (s *Server) Doctor(ctx context.Context) []doctor.Check {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	in := doctor.Input{
		PresetFile: s.Cfg.PresetFile, Service: s.Cfg.Router.Service, CacheDir: s.Cfg.Router.CacheDir,
		HFToken: s.Cfg.HFToken != "", Listen: s.Cfg.Listen, ListenAuth: s.Cfg.AuthToken != "" || s.Cfg.PasswordHash != "",
		AllowUnauthenticated: s.Cfg.AllowUnauthenticated, TLS: s.Cfg.TLSEnabled(),
		DiskLowGB: s.alertSettings().DiskFreeGB,
	}
	if in.RouterErr = s.Router.Health(ctx); in.RouterErr == nil {
		if raw, err := s.Router.Props(ctx); err == nil {
			var p struct {
				Role string `json:"role"`
			}
			_ = json.Unmarshal(raw, &p)
			in.RouterRole = p.Role
		}
	}
	if st, err := system.Status(ctx, s.Cfg.Router.Service); err != nil {
		in.ExecErr = err
	} else if st.ExecStart != "" {
		in.ExecStart = strings.Fields(st.ExecStart)
	}
	in.UnitEnv, _ = system.UnitEnv(ctx, s.Cfg.Router.Service)
	if cat, err := s.Catalog.Get(ctx); err == nil {
		in.Canonical = cat.Canonical
	}

	var liveIDs []string
	if list, err := s.Router.Models(ctx, false); err == nil {
		for _, m := range list {
			liveIDs = append(liveIDs, m.ID)
			if m.Status.Failed {
				in.FailedIDs = append(in.FailedIDs, m.ID)
			}
		}
	}
	if text, _, err := s.Apply.Read(); err != nil {
		in.PresetErr = err
	} else if issues, err := s.Apply.Validate(ctx, text, liveIDs); err != nil {
		in.ValidateErr = err
	} else {
		for _, i := range issues {
			in.Issues = append(in.Issues, doctor.Issue{Severity: i.Severity, Section: i.Section, Message: i.Message})
		}
	}

	d, err := models.DiskUsage(s.Cfg.Router.ModelsDir)
	in.DiskPath, in.DiskFree, in.DiskErr = d.Path, d.Free, err
	gs, err := gpu.Read(ctx)
	in.GPUCount, in.GPUErr = len(gs), err
	in.JournalErr = journalReadable(ctx, s.Cfg.Router.Service)
	return doctor.Run(in)
}

// journalReadable reads one journal line; journalctl exits 0 without permission and only prints
// a hint, so the output is checked too.
func journalReadable(ctx context.Context, unit string) error {
	var hint string
	err := system.Journal(ctx, unit, 1, false, func(l string) {
		if strings.Contains(l, "not seeing messages") || strings.Contains(l, "insufficient permissions") {
			hint = l
		}
	})
	if err == nil && hint != "" {
		return errors.New(strings.TrimSpace(hint))
	}
	return err
}

func (s *Server) doctor(w http.ResponseWriter, r *http.Request) {
	cs := s.Doctor(r.Context())
	writeJSON(w, 200, map[string]any{"checks": cs, "status": doctor.Worst(cs)})
}
