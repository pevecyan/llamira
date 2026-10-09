package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"llamira/internal/bench"
)

func (s *Server) runBench(w http.ResponseWriter, r *http.Request) {
	var req bench.Request
	if err := readJSON(r, &req); err != nil || req.Model == "" {
		writeErr(w, 400, "body must include \"model\"")
		return
	}
	// finish (and record) even if the client goes away
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 15*time.Minute)
	defer cancel()
	res, err := s.Bench.Run(ctx, req)
	if errors.Is(err, bench.ErrBusy) {
		writeErr(w, 409, "%v", err)
		return
	}
	if err != nil {
		writeErr(w, 502, "%v", err)
		return
	}
	id, _ := s.Store.AddBenchmark(res.Time, res.Model, res)
	s.Log.Info("benchmark", "model", res.Model, "pp_tps", res.PromptTPS, "tg_tps", res.GenTPS, "error", res.Error)
	writeJSON(w, 200, map[string]any{"id": id, "result": res})
}

func (s *Server) listBench(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListBenchmarks(500)
	if err != nil {
		writeErr(w, 500, "%v", err)
		return
	}
	writeJSON(w, 200, list)
}

func (s *Server) deleteBench(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err := s.Store.DeleteBenchmark(id); err != nil {
		writeErr(w, 500, "%v", err)
		return
	}
	writeJSON(w, 200, map[string]bool{"success": true})
}
