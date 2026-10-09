package api

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"

	"llamira/internal/schedule"
)

func (s *Server) getSchedules(w http.ResponseWriter, r *http.Request) {
	rs, err := s.Store.Schedules()
	if err != nil {
		writeErr(w, 500, "%v", err)
		return
	}
	writeJSON(w, 200, rs)
}

// putSchedules replaces the rule list (keeping run history of existing rules).
func (s *Server) putSchedules(w http.ResponseWriter, r *http.Request) {
	var rs []schedule.Rule
	if err := readJSON(r, &rs); err != nil {
		writeErr(w, 400, "%v", err)
		return
	}
	old, _ := s.Store.Schedules()
	prev := map[string]schedule.Rule{}
	for _, o := range old {
		prev[o.ID] = o
	}
	for i := range rs {
		if err := rs[i].Validate(); err != nil {
			writeErr(w, 400, "rule %d: %v", i+1, err)
			return
		}
		if rs[i].ID == "" {
			b := make([]byte, 4)
			rand.Read(b)
			rs[i].ID = hex.EncodeToString(b)
		} else if o, ok := prev[rs[i].ID]; ok {
			rs[i].LastRun, rs[i].LastResult = o.LastRun, o.LastResult
		}
	}
	if err := s.Store.SetSchedules(rs); err != nil {
		writeErr(w, 500, "%v", err)
		return
	}
	writeJSON(w, 200, rs)
}

func (s *Server) runSchedule(w http.ResponseWriter, r *http.Request) {
	res, err := s.Schedule.RunNow(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, 404, "%v", err)
		return
	}
	writeJSON(w, 200, map[string]string{"result": res})
}
