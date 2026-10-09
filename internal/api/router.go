package api

import (
	"context"
	"net/http"
	"time"

	"llamira/internal/install"
	"llamira/internal/system"
)

func (s *Server) getRouterSettings(w http.ResponseWriter, r *http.Request) {
	rs, err := install.ReadRouterSettings(r.Context(), s.Cfg.Router.Service)
	if err != nil {
		writeErr(w, 500, "%v", err)
		return
	}
	writeJSON(w, 200, rs)
}

func (s *Server) setRouterSetting(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if err := readJSON(r, &req); err != nil || req.Key == "" {
		writeErr(w, 400, "body must be {\"key\": ..., \"value\": ...}")
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Minute)
	defer cancel()
	out, err := system.Privileged(ctx, install.BinPath, "router-set", req.Key+"="+req.Value)
	if err != nil {
		writeErr(w, 400, "%v", err)
		return
	}
	s.Log.Info("router setting", "key", req.Key, "value", req.Value)
	writeJSON(w, 200, map[string]string{"output": out})
}

// getRouterKey reveals the router API key (so it can be copied into client configs).
func (s *Server) getRouterKey(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]string{"key": s.Router.Key()})
}
