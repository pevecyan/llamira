package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"llamira/internal/install"
	"llamira/internal/llamacpp"
	"llamira/internal/system"
)

func (s *Server) buildsDir() string {
	if s.Cfg.Router.BuildsDir != "" {
		return s.Cfg.Router.BuildsDir
	}
	return llamacpp.DefaultBuildsDir
}

// llamacppStatus lists builds, which one is active and which are building.
func (s *Server) llamacppStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	active := s.ActiveBinary(ctx)
	builds := llamacpp.ListBuilds(s.Cfg.Router.WorkDir, s.buildsDir())
	if builds == nil {
		builds = []llamacpp.Build{}
	}
	for i := range builds {
		builds[i].Active = builds[i].Binary == active
		if !builds[i].Original {
			builds[i].Running = system.IsActive(ctx, llamacpp.BuildUnit(builds[i].Ref))
		}
	}
	version := ""
	if cat, err := s.Catalog.Get(ctx); err == nil {
		version = cat.Version
	}
	// building needs a source checkout with a previous build's flags; prebuilt works everywhere
	_, cmakeErr := os.Stat(filepath.Join(s.Cfg.Router.WorkDir, "build", "CMakeCache.txt"))
	host := s.hostInfo()
	writeJSON(w, 200, map[string]any{"active_binary": active, "version": version, "builds": builds,
		"can_build": cmakeErr == nil, "flavor": host.Flavor, "cuda": host.CUDA})
}

func (s *Server) llamacppReleases(w http.ResponseWriter, r *http.Request) {
	rel, err := llamacpp.Releases(r.Context())
	if err != nil {
		writeErr(w, 502, "%v", err)
		return
	}
	writeJSON(w, 200, rel)
}

// llamacppOp runs build / cancel-build / remove-build / switch through the privileged helper.
func (s *Server) llamacppOp(w http.ResponseWriter, r *http.Request) {
	op := r.PathValue("op")
	switch op {
	case "build", "fetch", "cancel-build", "remove-build", "switch":
	default:
		writeErr(w, 404, "unknown operation %q", op)
		return
	}
	var req struct {
		Ref string `json:"ref"`
	}
	if err := readJSON(r, &req); err != nil || (req.Ref != "original" && !llamacpp.ValidRef(req.Ref)) {
		writeErr(w, 400, "ref must be a release tag (b1234), a commit hash, master or original")
		return
	}
	// a switch restarts the router; let it finish even if the client disconnects
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Minute)
	defer cancel()
	out, err := system.Privileged(ctx, install.BinPath, op, req.Ref)
	if err != nil {
		writeErr(w, 500, "%v", err)
		return
	}
	if op == "switch" {
		s.Catalog.Invalidate()
	}
	s.Log.Info("llama.cpp "+op, "ref", req.Ref)
	writeJSON(w, 200, map[string]string{"output": out})
}

// llamacppLog streams the journal of a build job (SSE when follow=1).
func (s *Server) llamacppLog(w http.ResponseWriter, r *http.Request) {
	ref := r.URL.Query().Get("ref")
	if !llamacpp.ValidRef(ref) {
		writeErr(w, 400, "invalid ref")
		return
	}
	unit := llamacpp.BuildUnit(ref)
	if r.URL.Query().Get("follow") == "" {
		var lines []string
		_ = system.Journal(r.Context(), unit, 400, false, func(l string) { lines = append(lines, l) })
		writeJSON(w, 200, map[string]any{"lines": lines})
		return
	}
	send, ok := sse(w)
	if !ok {
		return
	}
	_ = system.Journal(r.Context(), unit, 400, true, func(l string) { send("log", l) })
}

// UpdateInfo says whether a newer upstream llama.cpp release than the running build exists.
type UpdateInfo struct {
	Current        string    `json:"current"`                   // running build (b11036-911f6cdc8)
	CurrentVersion string    `json:"current_version,omitempty"` // newest version at or below it (v0.4.1)
	BuildsAhead    int       `json:"builds_ahead,omitempty"`    // builds after that version
	Latest         string    `json:"latest,omitempty"`          // build tag of the newest version (b11429)
	LatestVersion  string    `json:"latest_version,omitempty"`  // v0.6.0
	LatestBuild    string    `json:"latest_build,omitempty"`    // newest nightly build (b11521)
	Newer          bool      `json:"newer"`                     // the newest version is newer than the running build
	Published      time.Time `json:"published,omitempty"`
	URL            string    `json:"url,omitempty"`
	Notes          string    `json:"notes,omitempty"`
	Checked        time.Time `json:"checked"`
	Error          string    `json:"error,omitempty"`

	versions []llamacpp.Release
}

// CheckUpdate compares the running build with the newest version release (v0.6.0, …; nightly
// builds are reported but do not count as an update). GitHub is asked at most every 6 hours;
// the running build is read every time.
func (s *Server) CheckUpdate(ctx context.Context) UpdateInfo {
	s.updMu.Lock()
	defer s.updMu.Unlock()
	cur := ""
	if raw, err := s.Router.Props(ctx); err == nil {
		var p struct {
			BuildInfo string `json:"build_info"`
		}
		_ = json.Unmarshal(raw, &p)
		cur = p.BuildInfo
	}
	if cur == "" {
		if cat, err := s.Catalog.Get(ctx); err == nil {
			cur = cat.Version
		}
	}
	if s.upd.Checked.IsZero() || time.Since(s.upd.Checked) > 6*time.Hour {
		info := UpdateInfo{Checked: time.Now()}
		if vs, err := llamacpp.Versions(ctx); err != nil {
			info.Error = err.Error()
			info.Checked = time.Now().Add(-5*time.Hour - 30*time.Minute) // retry in ~30 min
		} else {
			latest := vs[0] // newest first
			info.Latest, info.LatestVersion = latest.Tag, latest.Version
			info.Published, info.URL, info.Notes = latest.Published, latest.URL, latest.Notes
			info.versions = vs
		}
		if rel, err := llamacpp.Releases(ctx); err == nil {
			for _, r := range rel {
				if llamacpp.BuildNumber(r.Tag) > llamacpp.BuildNumber(info.LatestBuild) {
					info.LatestBuild = r.Tag
				}
			}
		}
		s.upd = info
	}
	out := s.upd
	out.Current = cur
	c, l := llamacpp.BuildNumber(cur), llamacpp.BuildNumber(out.Latest)
	out.Newer = c > 0 && l > c
	out.CurrentVersion, out.BuildsAhead = llamacpp.VersionOf(out.versions, c)
	return out
}

func (s *Server) llamacppUpdate(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.CheckUpdate(r.Context()))
}

func (s *Server) llamacppVersions(w http.ResponseWriter, r *http.Request) {
	vs, err := llamacpp.Versions(r.Context())
	if err != nil {
		writeErr(w, 502, "%v", err)
		return
	}
	writeJSON(w, 200, vs)
}
