package llamacpp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DryRunOptions describes how to start a throwaway router instance.
type DryRunOptions struct {
	Binary    string
	WorkDir   string
	Env       []string // extra environment (e.g. LLAMA_CACHE=..., LD_LIBRARY_PATH=...)
	ModelsDir string
	Timeout   time.Duration
}

// DryRunResult is what the throwaway router reported for a preset file.
type DryRunResult struct {
	Models []Model `json:"models"`
	Log    string  `json:"log,omitempty"`
}

// DryRun starts `llama-server` in router mode on a free localhost port with the given preset
// text and autoload disabled, reads GET /models and stops it. No model is loaded, so no VRAM
// is used. It validates the preset with llama.cpp's own parser before the live router sees it.
func DryRun(ctx context.Context, opts DryRunOptions, presetText string) (*DryRunResult, error) {
	if opts.Timeout == 0 {
		opts.Timeout = 20 * time.Second
	}
	f, err := os.CreateTemp("", "llamira-dryrun-*.ini")
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(presetText); err != nil {
		f.Close()
		return nil, err
	}
	f.Close()

	port, err := freePort()
	if err != nil {
		return nil, err
	}
	args := []string{
		"--host", "127.0.0.1", "--port", strconv.Itoa(port),
		"--models-max", "1", "--no-models-autoload",
		"--models-preset", f.Name(),
	}
	if opts.ModelsDir != "" {
		args = append(args, "--models-dir", opts.ModelsDir)
	}
	cmd := exec.Command(opts.Binary, args...)
	cmd.Dir = opts.WorkDir
	cmd.Env = append(os.Environ(), opts.Env...)
	setProcessGroup(cmd)
	var log syncBuffer
	cmd.Stdout, cmd.Stderr = &log, &log
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start dry-run router: %w", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	defer func() {
		terminateGroup(cmd, false)
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
			terminateGroup(cmd, true)
			<-exited
		}
	}()

	client := NewClient("http://127.0.0.1:"+strconv.Itoa(port), "")
	client.HTTP.Timeout = 2 * time.Second
	deadline := time.Now().Add(opts.Timeout)
	for {
		select {
		case err := <-exited:
			exited <- err // let the deferred cleanup see it
			return &DryRunResult{Log: log.Tail(4000)}, fmt.Errorf("dry-run router exited: %s", lastError(log.String()))
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
		if client.Health(ctx) == nil {
			break
		}
		if time.Now().After(deadline) {
			return &DryRunResult{Log: log.Tail(4000)}, errors.New("dry-run router did not become healthy in time")
		}
	}
	models, err := client.Models(ctx, false)
	if err != nil {
		return &DryRunResult{Log: log.Tail(4000)}, fmt.Errorf("dry-run /models: %w", err)
	}
	return &DryRunResult{Models: models, Log: log.Tail(4000)}, nil
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// lastError picks the most relevant line from a llama-server log.
func lastError(log string) string {
	lines := strings.Split(strings.TrimSpace(log), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.ToLower(lines[i])
		if strings.Contains(l, "error") || strings.Contains(l, "not recognized") || strings.Contains(l, "exception") {
			return strings.TrimSpace(lines[i])
		}
	}
	if len(lines) > 0 {
		return strings.TrimSpace(lines[len(lines)-1])
	}
	return "no output"
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.buf.Len() > 1<<20 {
		return len(p), nil
	}
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *syncBuffer) Tail(n int) string {
	s := b.String()
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}
