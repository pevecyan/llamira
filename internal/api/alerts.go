package api

import (
	"net/http"
	"net/url"
	"strings"

	"llamira/internal/alerts"
)

func (s *Server) alertSettings() alerts.Settings {
	if st, ok := s.Store.AlertSettings(); ok {
		return st
	}
	return alerts.DefaultSettings()
}

func (s *Server) getAlerts(w http.ResponseWriter, r *http.Request) {
	recent, err := s.Store.Alerts(100)
	if err != nil {
		writeErr(w, 500, "%v", err)
		return
	}
	writeJSON(w, 200, map[string]any{"settings": s.alertSettings(), "recent": recent})
}

func (s *Server) putAlertSettings(w http.ResponseWriter, r *http.Request) {
	var st alerts.Settings
	if err := readJSON(r, &st); err != nil {
		writeErr(w, 400, "%v", err)
		return
	}
	for _, f := range []struct {
		name string
		v    *string
	}{{"webhook URL", &st.WebhookURL}, {"ntfy topic URL", &st.NtfyURL}} {
		*f.v = strings.TrimSpace(*f.v)
		if *f.v == "" {
			continue
		}
		u, err := url.Parse(*f.v)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			writeErr(w, 400, "%s must be an http(s) URL", f.name)
			return
		}
	}
	st.TelegramToken, st.TelegramChat = strings.TrimSpace(st.TelegramToken), strings.TrimSpace(st.TelegramChat)
	if (st.TelegramToken == "") != (st.TelegramChat == "") {
		writeErr(w, 400, "Telegram needs both the bot token and the chat id")
		return
	}
	if st.GPUVRAMPct < 0 || st.GPUVRAMPct > 100 {
		writeErr(w, 400, "GPU memory threshold must be 0–100 %%")
		return
	}
	if st.CooldownMin <= 0 {
		st.CooldownMin = 30
	}
	if err := s.Store.SetAlertSettings(st); err != nil {
		writeErr(w, 500, "%v", err)
		return
	}
	writeJSON(w, 200, st)
}

func (s *Server) testAlert(w http.ResponseWriter, r *http.Request) {
	e := s.Alerts.Send(r.Context(), s.alertSettings(), alerts.Event{Event: "test", Severity: "info",
		Message: "Test alert from llamira"})
	writeJSON(w, 200, e)
}
