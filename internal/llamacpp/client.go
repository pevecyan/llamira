package llamacpp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// Client talks to a llama-server running in router mode.
type Client struct {
	BaseURL string
	APIKey  string
	KeyFile string // optional: file holding the router API key (re-read when it changes)
	HTTP    *http.Client

	keyMu   sync.Mutex
	keyMod  time.Time
	keyFile string
}

// Key returns the API key to send: APIKey, or the first line of KeyFile if it exists.
func (c *Client) Key() string {
	if c.APIKey != "" || c.KeyFile == "" {
		return c.APIKey
	}
	st, err := os.Stat(c.KeyFile)
	if err != nil {
		return ""
	}
	c.keyMu.Lock()
	defer c.keyMu.Unlock()
	if !st.ModTime().Equal(c.keyMod) {
		b, err := os.ReadFile(c.KeyFile)
		if err != nil {
			return ""
		}
		c.keyFile, _, _ = strings.Cut(strings.TrimSpace(string(b)), "\n")
		c.keyMod = st.ModTime()
	}
	return c.keyFile
}

func NewClient(baseURL, apiKey string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}
}

// ModelStatus is the "status" object of a router model.
type ModelStatus struct {
	Value    string                     `json:"value"` // unloaded, loading, loaded, sleeping, downloading
	Args     []string                   `json:"args,omitempty"`
	Preset   string                     `json:"preset,omitempty"`
	Failed   bool                       `json:"failed,omitempty"`
	ExitCode *int                       `json:"exit_code,omitempty"`
	Progress map[string]json.RawMessage `json:"progress,omitempty"`
}

// Model is one entry of GET /models. Fields beyond the known ones are kept in Extra.
type Model struct {
	ID           string          `json:"id"`
	Aliases      []string        `json:"aliases"`
	Tags         []string        `json:"tags"`
	Status       ModelStatus     `json:"status"`
	Source       string          `json:"source"` // cache, dir, preset
	CanRemove    bool            `json:"can_remove"`
	Architecture json.RawMessage `json:"architecture,omitempty"`
	Meta         json.RawMessage `json:"meta,omitempty"`
}

// APIError is an OAI-style error returned by llama-server.
type APIError struct {
	Status  int
	Message string
	Type    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("llama-server: %s (HTTP %d)", e.Message, e.Status)
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if k := c.Key(); k != "" {
		req.Header.Set("Authorization", "Bearer "+k)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Error struct {
				Message string `json:"message"`
				Type    string `json:"type"`
			} `json:"error"`
		}
		if json.Unmarshal(data, &e) == nil && e.Error.Message != "" {
			return &APIError{Status: resp.StatusCode, Message: e.Error.Message, Type: e.Error.Type}
		}
		return &APIError{Status: resp.StatusCode, Message: strings.TrimSpace(string(data))}
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

// Health returns nil when GET /health reports ok.
func (c *Client) Health(ctx context.Context) error {
	var r struct {
		Status string `json:"status"`
	}
	if err := c.do(ctx, http.MethodGet, "/health", nil, &r); err != nil {
		return err
	}
	if r.Status != "ok" {
		return fmt.Errorf("health status %q", r.Status)
	}
	return nil
}

// Props returns GET /props (router-level properties) as raw JSON.
func (c *Client) Props(ctx context.Context) (json.RawMessage, error) {
	var r json.RawMessage
	err := c.do(ctx, http.MethodGet, "/props", nil, &r)
	return r, err
}

// Models returns GET /models. With reload, the router re-reads all sources including the preset file.
func (c *Client) Models(ctx context.Context, reload bool) ([]Model, error) {
	path := "/models"
	if reload {
		path += "?reload=1"
	}
	var r struct {
		Data []Model `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &r); err != nil {
		return nil, err
	}
	return r.Data, nil
}

// ModelsRaw returns the raw GET /models payload (for pass-through to the UI).
func (c *Client) ModelsRaw(ctx context.Context) (json.RawMessage, error) {
	var r json.RawMessage
	err := c.do(ctx, http.MethodGet, "/models", nil, &r)
	return r, err
}

func (c *Client) Load(ctx context.Context, model string) error {
	return c.do(ctx, http.MethodPost, "/models/load", map[string]string{"model": model}, nil)
}

// Unload unloads a running model, or cancels a download in progress.
func (c *Client) Unload(ctx context.Context, model string) error {
	return c.do(ctx, http.MethodPost, "/models/unload", map[string]string{"model": model}, nil)
}

// Download starts a background download of "org/repo:QUANT" into the router's cache.
func (c *Client) Download(ctx context.Context, model string) error {
	return c.do(ctx, http.MethodPost, "/models", map[string]string{"model": model}, nil)
}

// Delete removes a cache-sourced model.
func (c *Client) Delete(ctx context.Context, model string) error {
	return c.do(ctx, http.MethodDelete, "/models?model="+url.QueryEscape(model), nil, nil)
}

// Event is one message of GET /models/sse.
type Event struct {
	Model string          `json:"model"`
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data,omitempty"`
}

// Events subscribes to GET /models/sse and calls fn for every event until ctx is done
// or the stream ends. It returns the error that ended the stream.
func (c *Client) Events(ctx context.Context, fn func(Event)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/models/sse", nil)
	if err != nil {
		return err
	}
	if k := c.Key(); k != "" {
		req.Header.Set("Authorization", "Bearer "+k)
	}
	hc := &http.Client{Transport: c.HTTP.Transport}
	hc.Timeout = 0 // streaming
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("models/sse: HTTP %d", resp.StatusCode)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 4<<20)
	for sc.Scan() {
		line, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		var ev Event
		if json.Unmarshal([]byte(line), &ev) == nil {
			fn(ev)
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return io.EOF
}
