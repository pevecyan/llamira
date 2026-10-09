// Package store keeps llamira's own state (apply history) in SQLite.
package store

import (
	"database/sql"
	"encoding/json"
	"time"

	_ "modernc.org/sqlite"

	"llamira/internal/alerts"
	"llamira/internal/bench"
	"llamira/internal/schedule"
	"llamira/internal/usage"
)

type Store struct{ db *sql.DB }

const schema = `
CREATE TABLE IF NOT EXISTS applies (
	id      INTEGER PRIMARY KEY AUTOINCREMENT,
	time    TEXT NOT NULL,
	ok      INTEGER NOT NULL,
	summary TEXT NOT NULL,
	error   TEXT NOT NULL DEFAULT '',
	steps   TEXT NOT NULL DEFAULT '[]',
	backup  TEXT NOT NULL DEFAULT '',
	before  TEXT NOT NULL,
	after   TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS requests (
	t         INTEGER NOT NULL, -- unix seconds
	model     TEXT NOT NULL,
	prompt_n  INTEGER NOT NULL,
	prompt_ms REAL NOT NULL,
	gen_n     INTEGER NOT NULL,
	gen_ms    REAL NOT NULL
);
CREATE INDEX IF NOT EXISTS requests_t ON requests(t);
CREATE TABLE IF NOT EXISTS kv (k TEXT PRIMARY KEY, v TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS alerts (
	id    INTEGER PRIMARY KEY AUTOINCREMENT,
	t     INTEGER NOT NULL,
	event TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS sweeps (
	id    INTEGER PRIMARY KEY AUTOINCREMENT,
	t     INTEGER NOT NULL,
	sweep TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS benchmarks (
	id     INTEGER PRIMARY KEY AUTOINCREMENT,
	time   TEXT NOT NULL,
	model  TEXT NOT NULL,
	result TEXT NOT NULL
);`

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Apply is one recorded apply attempt.
type Apply struct {
	ID      int64           `json:"id"`
	Time    time.Time       `json:"time"`
	OK      bool            `json:"ok"`
	Summary string          `json:"summary"`
	Error   string          `json:"error,omitempty"`
	Steps   json.RawMessage `json:"steps"`
	Backup  string          `json:"backup,omitempty"`
	Before  string          `json:"before,omitempty"`
	After   string          `json:"after,omitempty"`
}

func (s *Store) AddApply(a Apply) (int64, error) {
	if a.Steps == nil {
		a.Steps = json.RawMessage("[]")
	}
	res, err := s.db.Exec(`INSERT INTO applies (time, ok, summary, error, steps, backup, before, after) VALUES (?,?,?,?,?,?,?,?)`,
		a.Time.UTC().Format(time.RFC3339), a.OK, a.Summary, a.Error, string(a.Steps), a.Backup, a.Before, a.After)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ListApplies returns the most recent applies without the file contents.
func (s *Store) ListApplies(limit int) ([]Apply, error) {
	rows, err := s.db.Query(`SELECT id, time, ok, summary, error, steps, backup FROM applies ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Apply
	for rows.Next() {
		var a Apply
		var t, steps string
		if err := rows.Scan(&a.ID, &t, &a.OK, &a.Summary, &a.Error, &steps, &a.Backup); err != nil {
			return nil, err
		}
		a.Time, _ = time.Parse(time.RFC3339, t)
		a.Steps = json.RawMessage(steps)
		out = append(out, a)
	}
	return out, rows.Err()
}

// GetApply returns one apply including the before/after file contents.
func (s *Store) GetApply(id int64) (Apply, error) {
	var a Apply
	var t, steps string
	err := s.db.QueryRow(`SELECT id, time, ok, summary, error, steps, backup, before, after FROM applies WHERE id = ?`, id).
		Scan(&a.ID, &t, &a.OK, &a.Summary, &a.Error, &steps, &a.Backup, &a.Before, &a.After)
	a.Time, _ = time.Parse(time.RFC3339, t)
	a.Steps = json.RawMessage(steps)
	return a, err
}

// Benchmark is a stored speed test result (the result is opaque JSON for the store).
type Benchmark struct {
	ID     int64           `json:"id"`
	Result json.RawMessage `json:"result"`
}

func (s *Store) AddBenchmark(t time.Time, model string, result any) (int64, error) {
	b, err := json.Marshal(result)
	if err != nil {
		return 0, err
	}
	res, err := s.db.Exec(`INSERT INTO benchmarks (time, model, result) VALUES (?,?,?)`,
		t.UTC().Format(time.RFC3339), model, string(b))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) ListBenchmarks(limit int) ([]Benchmark, error) {
	rows, err := s.db.Query(`SELECT id, result FROM benchmarks ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Benchmark{}
	for rows.Next() {
		var b Benchmark
		var r string
		if err := rows.Scan(&b.ID, &r); err != nil {
			return nil, err
		}
		b.Result = json.RawMessage(r)
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Store) DeleteBenchmark(id int64) error {
	_, err := s.db.Exec(`DELETE FROM benchmarks WHERE id = ?`, id)
	return err
}

// ---- usage (requests parsed from the router journal) -----------------------------------

// AddRequests stores parsed requests (usage.Sink).
func (s *Store) AddRequests(rs []usage.Request) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	st, err := tx.Prepare(`INSERT INTO requests (t, model, prompt_n, prompt_ms, gen_n, gen_ms) VALUES (?,?,?,?,?,?)`)
	if err != nil {
		tx.Rollback()
		return err
	}
	for _, r := range rs {
		if _, err := st.Exec(r.Time.Unix(), r.Model, r.PromptN, r.PromptMS, r.GenN, r.GenMS); err != nil {
			tx.Rollback()
			return err
		}
	}
	st.Close()
	return tx.Commit()
}

func (s *Store) Cursor() string {
	var v string
	_ = s.db.QueryRow(`SELECT v FROM kv WHERE k = 'journal_cursor'`).Scan(&v)
	return v
}

func (s *Store) SetCursor(c string) error {
	_, err := s.db.Exec(`INSERT INTO kv (k, v) VALUES ('journal_cursor', ?) ON CONFLICT(k) DO UPDATE SET v = excluded.v`, c)
	return err
}

// ResetUsage clears parsed requests and the journal cursor when the parser version changed,
// so the journal is parsed again from scratch.
func (s *Store) ResetUsage(version string) error {
	var v string
	_ = s.db.QueryRow(`SELECT v FROM kv WHERE k = 'usage_version'`).Scan(&v)
	if v == version {
		return nil
	}
	if _, err := s.db.Exec(`DELETE FROM requests; DELETE FROM kv WHERE k = 'journal_cursor'`); err != nil {
		return err
	}
	_, err := s.db.Exec(`INSERT INTO kv (k, v) VALUES ('usage_version', ?) ON CONFLICT(k) DO UPDATE SET v = excluded.v`, version)
	return err
}

// PruneRequests deletes requests older than keep.
func (s *Store) PruneRequests(keep time.Duration) error {
	_, err := s.db.Exec(`DELETE FROM requests WHERE t < ?`, time.Now().Add(-keep).Unix())
	return err
}

// ModelUsage aggregates requests per model.
type ModelUsage struct {
	Model     string    `json:"model"`
	Requests  int64     `json:"requests"`
	PromptN   int64     `json:"prompt_tokens"`
	GenN      int64     `json:"gen_tokens"`
	PromptTPS float64   `json:"prompt_tps"` // token-weighted average
	GenTPS    float64   `json:"gen_tps"`
	LastUsed  time.Time `json:"last_used"`
}

// Bucket is one time bucket (day or hour) for one model.
type Bucket struct {
	Start    time.Time `json:"start"`
	Model    string    `json:"model"`
	Requests int64     `json:"requests"`
	GenN     int64     `json:"gen_tokens"`
	PromptN  int64     `json:"prompt_tokens"`
}

func (s *Store) Usage(since time.Time) ([]ModelUsage, error) {
	rows, err := s.db.Query(`SELECT model, COUNT(*), SUM(prompt_n), SUM(gen_n), SUM(prompt_ms), SUM(gen_ms), MAX(t)
		FROM requests WHERE t >= ? GROUP BY model ORDER BY MAX(t) DESC`, since.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ModelUsage{}
	for rows.Next() {
		var u ModelUsage
		var pms, gms float64
		var last int64
		if err := rows.Scan(&u.Model, &u.Requests, &u.PromptN, &u.GenN, &pms, &gms, &last); err != nil {
			return nil, err
		}
		if pms > 0 {
			u.PromptTPS = float64(u.PromptN) / pms * 1000
		}
		if gms > 0 {
			u.GenTPS = float64(u.GenN) / gms * 1000
		}
		u.LastUsed = time.Unix(last, 0)
		out = append(out, u)
	}
	return out, rows.Err()
}

// Buckets groups requests into buckets of the given size (seconds), in local time.
func (s *Store) Buckets(since time.Time, size int64) ([]Bucket, error) {
	_, off := time.Now().Zone()
	rows, err := s.db.Query(`SELECT ((t + ?) / ?) * ? - ? AS b, model, COUNT(*), SUM(gen_n), SUM(prompt_n)
		FROM requests WHERE t >= ? GROUP BY b, model ORDER BY b`, off, size, size, off, since.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Bucket{}
	for rows.Next() {
		var b Bucket
		var start int64
		if err := rows.Scan(&start, &b.Model, &b.Requests, &b.GenN, &b.PromptN); err != nil {
			return nil, err
		}
		b.Start = time.Unix(start, 0)
		out = append(out, b)
	}
	return out, rows.Err()
}

// ---- alerts -------------------------------------------------------------------------------

func (s *Store) AlertSettings() (alerts.Settings, bool) {
	var v string
	if s.db.QueryRow(`SELECT v FROM kv WHERE k = 'alert_settings'`).Scan(&v) != nil {
		return alerts.Settings{}, false
	}
	st := alerts.DefaultSettings() // fields added later keep their defaults
	if json.Unmarshal([]byte(v), &st) != nil {
		return alerts.Settings{}, false
	}
	return st, true
}

func (s *Store) SetAlertSettings(st alerts.Settings) error {
	b, _ := json.Marshal(st)
	_, err := s.db.Exec(`INSERT INTO kv (k, v) VALUES ('alert_settings', ?) ON CONFLICT(k) DO UPDATE SET v = excluded.v`, string(b))
	return err
}

func (s *Store) AddAlert(e alerts.Event) error {
	b, _ := json.Marshal(e)
	if _, err := s.db.Exec(`INSERT INTO alerts (t, event) VALUES (?, ?)`, e.Time.Unix(), string(b)); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM alerts WHERE id <= (SELECT MAX(id) FROM alerts) - 500`)
	return err
}

func (s *Store) Alerts(limit int) ([]json.RawMessage, error) {
	rows, err := s.db.Query(`SELECT event FROM alerts ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []json.RawMessage{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, json.RawMessage(v))
	}
	return out, rows.Err()
}

// ---- schedules (kv JSON) ------------------------------------------------------------------

func (s *Store) Schedules() ([]schedule.Rule, error) {
	var v string
	if err := s.db.QueryRow(`SELECT v FROM kv WHERE k = 'schedules'`).Scan(&v); err != nil {
		return []schedule.Rule{}, nil
	}
	var rs []schedule.Rule
	if err := json.Unmarshal([]byte(v), &rs); err != nil {
		return nil, err
	}
	return rs, nil
}

func (s *Store) SetSchedules(rs []schedule.Rule) error {
	b, _ := json.Marshal(rs)
	_, err := s.db.Exec(`INSERT INTO kv (k, v) VALUES ('schedules', ?) ON CONFLICT(k) DO UPDATE SET v = excluded.v`, string(b))
	return err
}

// ---- power sweeps -------------------------------------------------------------------------

func (s *Store) SetSweepGuard(orig map[int]float64) error {
	b, _ := json.Marshal(orig)
	_, err := s.db.Exec(`INSERT INTO kv (k, v) VALUES ('sweep_guard', ?) ON CONFLICT(k) DO UPDATE SET v = excluded.v`, string(b))
	return err
}

func (s *Store) SweepGuard() (map[int]float64, bool) {
	var v string
	if s.db.QueryRow(`SELECT v FROM kv WHERE k = 'sweep_guard'`).Scan(&v) != nil {
		return nil, false
	}
	m := map[int]float64{}
	if json.Unmarshal([]byte(v), &m) != nil || len(m) == 0 {
		return nil, false
	}
	return m, true
}

func (s *Store) ClearSweepGuard() error {
	_, err := s.db.Exec(`DELETE FROM kv WHERE k = 'sweep_guard'`)
	return err
}

func (s *Store) AddSweep(sw *bench.Sweep) (int64, error) {
	b, _ := json.Marshal(sw)
	res, err := s.db.Exec(`INSERT INTO sweeps (t, sweep) VALUES (?, ?)`, sw.Started.Unix(), string(b))
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	sw.ID = id
	b, _ = json.Marshal(sw)
	_, _ = s.db.Exec(`UPDATE sweeps SET sweep = ? WHERE id = ?`, string(b), id)
	return id, nil
}

func (s *Store) Sweeps(limit int) ([]json.RawMessage, error) {
	rows, err := s.db.Query(`SELECT sweep FROM sweeps ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []json.RawMessage{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, json.RawMessage(v))
	}
	return out, rows.Err()
}

// ---- small key/value state ----------------------------------------------------------------

// Value returns a stored value ("" if unset).
func (s *Store) Value(key string) string {
	var v string
	_ = s.db.QueryRow(`SELECT v FROM kv WHERE k = ?`, "x:"+key).Scan(&v)
	return v
}

func (s *Store) SetValue(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO kv (k, v) VALUES (?, ?) ON CONFLICT(k) DO UPDATE SET v = excluded.v`, "x:"+key, value)
	return err
}

// FirstRequest is the time of the oldest stored request (zero if there are none).
func (s *Store) FirstRequest() time.Time {
	var t sql.NullInt64
	_ = s.db.QueryRow(`SELECT MIN(t) FROM requests`).Scan(&t)
	if !t.Valid {
		return time.Time{}
	}
	return time.Unix(t.Int64, 0)
}
