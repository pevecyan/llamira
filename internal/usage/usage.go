// Package usage derives per-request statistics from the llama-server journal: the router
// logs which model (port) serves each request, and the model instance logs its timings.
package usage

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Request is one completed request.
type Request struct {
	Time     time.Time
	Model    string
	PromptN  int
	PromptMS float64
	GenN     int
	GenMS    float64
}

var (
	reProxy = regexp.MustCompile(`proxying request to model (\S+) on port (\d+)`)
	rePort  = regexp.MustCompile(`^\[(\d+)\]`)
	reTask  = regexp.MustCompile(`print_timing: id\s+\d+ \| task (\d+) \|\s+(prompt eval|eval|total) time =\s+([\d.]+) ms /\s+(\d+) tokens`)
)

// Parser turns journal lines into Requests. It is not safe for concurrent use.
type Parser struct {
	portModel map[string]string
	pending   map[string]*Request // port/task → partial request
}

func NewParser() *Parser {
	return &Parser{portModel: map[string]string{}, pending: map[string]*Request{}}
}

// Line consumes one log message; it returns a Request when one completes.
func (p *Parser) Line(t time.Time, msg string) *Request {
	if m := reProxy.FindStringSubmatch(msg); m != nil {
		p.portModel[m[2]] = m[1]
		return nil
	}
	pm := rePort.FindStringSubmatch(msg)
	if pm == nil {
		return nil
	}
	m := reTask.FindStringSubmatch(msg)
	if m == nil {
		return nil
	}
	port, task, kind := pm[1], m[1], m[2]
	ms, _ := strconv.ParseFloat(m[3], 64)
	n, _ := strconv.Atoi(m[4])
	key := port + "/" + task
	r := p.pending[key]
	if r == nil {
		r = &Request{Model: p.portModel[port]}
		p.pending[key] = r
	}
	switch kind {
	case "prompt eval":
		r.PromptN, r.PromptMS = n, ms
	case "eval":
		r.GenN, r.GenMS = n, ms
	case "total":
		delete(p.pending, key)
		r.Time = t
		if r.Model == "" {
			r.Model = "(unknown)"
		}
		if len(p.pending) > 1000 { // abandoned partials (instance restarts)
			p.pending = map[string]*Request{}
		}
		return r
	}
	return nil
}

// Sink stores requests and the journal cursor.
type Sink interface {
	AddRequests([]Request) error
	SetCursor(string) error
	Cursor() string
}

// Follow reads the unit's journal from the stored cursor (or the last `backfill` on first
// run) and keeps following it until ctx is done.
func Follow(ctx context.Context, unit string, backfill time.Duration, sink Sink, log *slog.Logger) {
	for ctx.Err() == nil {
		if err := follow(ctx, unit, backfill, sink); err != nil && ctx.Err() == nil {
			log.Warn("usage collector", "error", err)
		}
		select {
		case <-ctx.Done():
		case <-time.After(10 * time.Second):
		}
	}
}

// follow first catches up without -f (journalctl -f does not replay a long backlog), then
// follows new entries from the last cursor.
func follow(ctx context.Context, unit string, backfill time.Duration, sink Sink) error {
	p := NewParser()
	if err := read(ctx, unit, backfill, sink, p, false); err != nil {
		return err
	}
	return read(ctx, unit, backfill, sink, p, true)
}

func read(ctx context.Context, unit string, backfill time.Duration, sink Sink, p *Parser, tail bool) error {
	args := []string{"-u", unit, "-o", "json", "--output-fields=MESSAGE", "--no-pager"}
	if tail {
		args = append(args, "-f", "--lines=all")
	}
	if c := sink.Cursor(); c != "" {
		args = append(args, "--after-cursor", c)
	} else {
		args = append(args, "--since", time.Now().Add(-backfill).Format("2006-01-02 15:04:05"))
	}
	cmd := exec.CommandContext(ctx, "journalctl", args...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	var batch []Request
	var cursor string
	flush := func() error {
		if len(batch) > 0 {
			if err := sink.AddRequests(batch); err != nil {
				return err
			}
			batch = batch[:0]
		}
		if cursor != "" {
			return sink.SetCursor(cursor)
		}
		return nil
	}
	lines := make(chan []byte, 256)
	go func() {
		sc := bufio.NewScanner(out)
		sc.Buffer(make([]byte, 256*1024), 4<<20)
		for sc.Scan() {
			lines <- append([]byte(nil), sc.Bytes()...)
		}
		close(lines)
	}()
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		select {
		case b, ok := <-lines:
			if !ok {
				_ = flush()
				return cmd.Wait()
			}
			var e struct {
				Cursor  string `json:"__CURSOR"`
				TS      string `json:"__REALTIME_TIMESTAMP"`
				Message any    `json:"MESSAGE"` // string, or byte array for non-UTF-8 messages
			}
			if json.Unmarshal(b, &e) != nil {
				continue
			}
			cursor = e.Cursor
			msg, ok := e.Message.(string)
			if !ok {
				continue
			}
			us, _ := strconv.ParseInt(e.TS, 10, 64)
			if !strings.Contains(msg, "print_timing") && !strings.Contains(msg, "proxying request") {
				continue
			}
			if r := p.Line(time.UnixMicro(us), msg); r != nil {
				batch = append(batch, *r)
				if len(batch) >= 500 {
					if err := flush(); err != nil {
						return err
					}
				}
			}
		case <-tick.C:
			if err := flush(); err != nil {
				return err
			}
		case <-ctx.Done():
			_ = flush()
			return ctx.Err()
		}
	}
}
