package alerts

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type memStore struct {
	mu     sync.Mutex
	events []Event
	kv     map[string]string
}

func (m *memStore) AlertSettings() (Settings, bool) { return Settings{}, false }
func (m *memStore) AddAlert(e Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, e)
	return nil
}
func (m *memStore) Value(k string) string { return m.kv[k] }
func (m *memStore) SetValue(k, v string) error {
	if m.kv == nil {
		m.kv = map[string]string{}
	}
	m.kv[k] = v
	return nil
}

type hit struct {
	path, body string
	header     http.Header
}

func recorder(t *testing.T, status int) (*httptest.Server, *[]hit) {
	var hits []hit
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		hits = append(hits, hit{r.URL.Path, string(b), r.Header.Clone()})
		mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func TestSendAllChannels(t *testing.T) {
	srv, hits := recorder(t, 200)
	TelegramAPI = srv.URL
	st := &memStore{}
	m := &Monitor{Store: st, host: "llama"}
	s := Settings{WebhookURL: srv.URL + "/hook", NtfyURL: srv.URL + "/topic", NtfyToken: "tk", TelegramToken: "123:abc", TelegramChat: "42"}
	e := m.Send(context.Background(), s, Event{Event: "gpu_temp", Severity: "warning", Message: "GPU 0 at 90 °C"})
	if !e.Sent || e.Error != "" || strings.Join(e.Channels, ",") != "ntfy,telegram,webhook" {
		t.Fatalf("event %+v", e)
	}
	by := map[string]hit{}
	for _, h := range *hits {
		by[h.path] = h
	}
	var hook map[string]any
	if json.Unmarshal([]byte(by["/hook"].body), &hook) != nil || hook["event"] != "gpu_temp" || hook["host"] != "llama" {
		t.Errorf("webhook body %q", by["/hook"].body)
	}
	if _, ok := hook["channels"]; ok {
		t.Error("webhook payload must not contain delivery bookkeeping")
	}
	n := by["/topic"]
	if n.body != "GPU 0 at 90 °C" || n.header.Get("Priority") != "high" || n.header.Get("Authorization") != "Bearer tk" || !strings.Contains(n.header.Get("Title"), "gpu_temp") {
		t.Errorf("ntfy %+v", n)
	}
	tg := by["/bot123:abc/sendMessage"]
	if !strings.Contains(tg.body, `"chat_id":"42"`) || !strings.Contains(tg.body, "GPU 0 at 90") {
		t.Errorf("telegram %q", tg.body)
	}
	if len(st.events) != 1 {
		t.Errorf("stored %d events", len(st.events))
	}
}

func TestSendFailureHidesTelegramToken(t *testing.T) {
	srv, _ := recorder(t, 401)
	TelegramAPI = srv.URL
	m := &Monitor{Store: &memStore{}}
	e := m.Send(context.Background(), Settings{TelegramToken: "123:secret", TelegramChat: "1"}, Event{Event: "test", Severity: "info"})
	if e.Sent || !strings.Contains(e.Error, "telegram: HTTP 401") || strings.Contains(e.Error, "secret") {
		t.Errorf("event %+v", e)
	}
	e = m.Send(context.Background(), Settings{}, Event{Event: "test"})
	if e.Sent || e.Error != "no delivery channel configured" {
		t.Errorf("no channels: %+v", e)
	}
}

func TestScheduleResult(t *testing.T) {
	st := &memStore{}
	m := &Monitor{Store: st, last: map[string]time.Time{}}
	// defaults (no stored settings) alert on failed schedules, but there is no channel: stored only
	m.ScheduleResult(context.Background(), "r1", "load", "a/b:Q4", "load ok: a/b:Q4")
	m.ScheduleResult(context.Background(), "r1", "load", "a/b:Q4", "load failed: model not found")
	m.ScheduleResult(context.Background(), "r1", "load", "a/b:Q4", "load failed: model not found") // cooldown
	if len(st.events) != 1 || st.events[0].Event != "schedule_failed" || !strings.Contains(st.events[0].Message, "scheduled load failed") {
		t.Fatalf("events %+v", st.events)
	}
}
