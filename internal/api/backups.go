package api

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type backupInfo struct {
	Name string    `json:"name"`
	Time time.Time `json:"time"`
	Size int64     `json:"size"`
}

// listBackups returns preset backups, newest first.
func (s *Server) listBackups(w http.ResponseWriter, r *http.Request) {
	out := []backupInfo{}
	for _, p := range s.Apply.Backups() {
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		out = append(out, backupInfo{Name: filepath.Base(p), Time: st.ModTime(), Size: st.Size()})
	}
	writeJSON(w, 200, out)
}

// getBackup returns the text of one backup (by file name inside the backup directory).
func (s *Server) getBackup(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !strings.HasPrefix(name, "models.ini.") || strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		writeErr(w, 400, "invalid backup name")
		return
	}
	b, err := os.ReadFile(filepath.Join(s.Apply.BackupDir, name))
	if err != nil {
		writeErr(w, 404, "backup %s not found", name)
		return
	}
	writeJSON(w, 200, map[string]string{"name": name, "text": string(b)})
}
