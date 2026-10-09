package api

import (
	"fmt"
	"net/http"

	"llamira/internal/gpu"
	"llamira/internal/install"
	"llamira/internal/system"
)

func (s *Server) getPower(w http.ResponseWriter, r *http.Request) {
	ps, err := gpu.PowerLimits(r.Context())
	if err != nil {
		writeErr(w, 500, "%v", err)
		return
	}
	persisted := install.LoadPowerLimits()
	type row struct {
		gpu.Power
		Persisted bool `json:"persisted"`
	}
	out := make([]row, len(ps))
	for i, p := range ps {
		_, ok := persisted[p.Index]
		out[i] = row{p, ok}
	}
	writeJSON(w, 200, out)
}

func (s *Server) setPower(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Index int     `json:"index"`
		Watts float64 `json:"watts"` // 0 = reset to default
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "%v", err)
		return
	}
	spec := fmt.Sprintf("%d:%.0f", req.Index, req.Watts)
	if req.Watts == 0 {
		spec = fmt.Sprintf("%d:default", req.Index)
	}
	out, err := system.Privileged(r.Context(), install.BinPath, "power", spec)
	if err != nil {
		writeErr(w, 400, "%v", err)
		return
	}
	s.Log.Info("gpu power limit", "spec", spec)
	writeJSON(w, 200, map[string]string{"output": out})
}
