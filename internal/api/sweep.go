package api

import (
	"errors"
	"net/http"

	"llamira/internal/bench"
)

// getSweep returns the running (or last) sweep and the saved ones.
func (s *Server) getSweep(w http.ResponseWriter, r *http.Request) {
	past, err := s.Store.Sweeps(50)
	if err != nil {
		writeErr(w, 500, "%v", err)
		return
	}
	writeJSON(w, 200, map[string]any{"current": s.Sweeper.Current(), "saved": past})
}

func (s *Server) startSweep(w http.ResponseWriter, r *http.Request) {
	var req bench.SweepRequest
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "%v", err)
		return
	}
	if req.MaxTempC == 0 {
		if st, ok := s.Store.AlertSettings(); ok && st.GPUTempC > 0 {
			req.MaxTempC = st.GPUTempC
		}
	}
	sw, err := s.Sweeper.Start(req)
	if errors.Is(err, bench.ErrBusy) {
		writeErr(w, 409, "%v", err)
		return
	}
	if err != nil {
		writeErr(w, 400, "%v", err)
		return
	}
	s.Log.Info("power sweep started", "model", req.Model, "plan", sw.Plan)
	writeJSON(w, 200, sw)
}

func (s *Server) cancelSweep(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]bool{"cancelled": s.Sweeper.Cancel()})
}
