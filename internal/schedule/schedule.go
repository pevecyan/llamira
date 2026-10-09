// Package schedule runs timed actions (load / unload models) evaluated in each rule's time zone.
package schedule

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // rules use the browser's IANA zone; the container may only have UTC

	"llamira/internal/llamacpp"
)

// Rule is one scheduled action.
type Rule struct {
	ID         string    `json:"id"`
	Enabled    bool      `json:"enabled"`
	Time       string    `json:"time"`   // HH:MM
	Days       []int     `json:"days"`   // 0 = Sunday … 6 = Saturday; empty = every day
	TZ         string    `json:"tz"`     // IANA zone, e.g. Europe/Ljubljana
	Action     string    `json:"action"` // load, unload, unload-all
	Model      string    `json:"model,omitempty"`
	LastRun    time.Time `json:"last_run,omitempty"`
	LastResult string    `json:"last_result,omitempty"`
}

// Validate checks a rule.
func (r *Rule) Validate() error {
	if _, err := time.Parse("15:04", r.Time); err != nil {
		return fmt.Errorf("time must be HH:MM")
	}
	if _, err := time.LoadLocation(r.TZ); err != nil || r.TZ == "" {
		return fmt.Errorf("unknown time zone %q", r.TZ)
	}
	switch r.Action {
	case "load", "unload":
		if r.Model == "" {
			return fmt.Errorf("%s needs a model", r.Action)
		}
	case "unload-all":
	default:
		return fmt.Errorf("unknown action %q", r.Action)
	}
	for _, d := range r.Days {
		if d < 0 || d > 6 {
			return fmt.Errorf("days must be 0–6")
		}
	}
	return nil
}

// Due reports whether the rule should fire at now (same minute, right weekday, not yet run).
func (r *Rule) Due(now time.Time) bool {
	if !r.Enabled {
		return false
	}
	loc, err := time.LoadLocation(r.TZ)
	if err != nil {
		return false
	}
	t := now.In(loc)
	if t.Format("15:04") != r.Time {
		return false
	}
	if len(r.Days) > 0 && !slices.Contains(r.Days, int(t.Weekday())) {
		return false
	}
	return r.LastRun.IsZero() || now.Sub(r.LastRun) > 90*time.Second
}

// Store persists rules.
type Store interface {
	Schedules() ([]Rule, error)
	SetSchedules([]Rule) error
}

type Runner struct {
	Router *llamacpp.Client
	Store  Store
	Log    *slog.Logger
	// OnResult is called after a rule ran on schedule (e.g. to alert on failures).
	OnResult func(ctx context.Context, r Rule, result string)
	mu       sync.Mutex
}

// Run checks rules every 20 seconds.
func (s *Runner) Run(ctx context.Context) {
	t := time.NewTicker(20 * time.Second)
	defer t.Stop()
	for {
		s.tick(ctx, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Runner) tick(ctx context.Context, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rules, err := s.Store.Schedules()
	if err != nil {
		return
	}
	changed := false
	for i := range rules {
		if rules[i].Due(now) {
			rules[i].LastRun = now
			rules[i].LastResult = s.exec(ctx, rules[i])
			changed = true
			if s.OnResult != nil {
				s.OnResult(ctx, rules[i], rules[i].LastResult)
			}
		}
	}
	if changed {
		_ = s.Store.SetSchedules(rules)
	}
}

// RunNow executes a rule immediately (from the UI) and records the result.
func (s *Runner) RunNow(ctx context.Context, id string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rules, err := s.Store.Schedules()
	if err != nil {
		return "", err
	}
	for i := range rules {
		if rules[i].ID == id {
			rules[i].LastRun = time.Now()
			rules[i].LastResult = s.exec(ctx, rules[i])
			return rules[i].LastResult, s.Store.SetSchedules(rules)
		}
	}
	return "", fmt.Errorf("rule %s not found", id)
}

func (s *Runner) exec(ctx context.Context, r Rule) string {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	var err error
	var done []string
	switch r.Action {
	case "load":
		err = s.Router.Load(ctx, r.Model)
		done = []string{r.Model}
	case "unload":
		err = s.Router.Unload(ctx, r.Model)
		done = []string{r.Model}
	case "unload-all":
		var ms []llamacpp.Model
		if ms, err = s.Router.Models(ctx, false); err == nil {
			for _, m := range ms {
				if v := m.Status.Value; v == "loaded" || v == "loading" || v == "sleeping" {
					if e := s.Router.Unload(ctx, m.ID); e != nil {
						err = e
					}
					done = append(done, m.ID)
				}
			}
		}
	}
	res := fmt.Sprintf("%s ok", r.Action)
	if len(done) > 0 {
		res += ": " + strings.Join(done, ", ")
	} else if r.Action == "unload-all" {
		res += ": nothing was loaded"
	}
	if err != nil {
		// "already running" / "not running" are fine for a schedule
		msg := err.Error()
		if strings.Contains(msg, "already running") || strings.Contains(msg, "not running") {
			res = r.Action + ": nothing to do (" + msg + ")"
		} else {
			res = r.Action + " failed: " + msg
		}
	}
	if s.Log != nil {
		s.Log.Info("schedule", "rule", r.ID, "result", res)
	}
	return res
}
