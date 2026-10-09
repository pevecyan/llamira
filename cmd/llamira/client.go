package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"llamira/internal/apply"
	"llamira/internal/config"
	"llamira/internal/gpu"
	"llamira/internal/llamacpp"
	"llamira/internal/presets"
	"llamira/internal/remote"
	"llamira/internal/system"
)

// apiClient calls a llamira API, locally via the unix socket or remotely over SSH.
type apiClient struct {
	http  *http.Client
	close func()
}

func newAPIClient(host string) (*apiClient, error) {
	if host == "" {
		if _, err := os.Stat(config.DefaultSocket); err == nil {
			tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", config.DefaultSocket)
			}}
			return &apiClient{http: &http.Client{Transport: tr}, close: func() {}}, nil
		}
	}
	cc, err := loadClientConfig()
	if err != nil {
		return nil, err
	}
	name, h, err := cc.Resolve(host)
	if err != nil {
		return nil, err
	}
	conn := remote.NewConn(name, h)
	return &apiClient{http: &http.Client{Transport: conn.Transport()}, close: conn.Close}, nil
}

func (c *apiClient) do(method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, "http://llamira/api"+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			return errors.New(e.Error)
		}
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

func hostFlags(name string) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	host := fs.String("host", "", "host name or user@host (default: local socket or default host)")
	return fs, host
}

func withClient(host string, fn func(*apiClient) error) error {
	c, err := newAPIClient(host)
	if err != nil {
		return err
	}
	defer c.close()
	return fn(c)
}

func cmdStatus(args []string) error {
	fs, host := hostFlags("status")
	asJSON := jsonFlag(fs)
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	return withClient(*host, func(c *apiClient) error {
		var st struct {
			Router struct {
				Healthy bool            `json:"healthy"`
				Error   string          `json:"error"`
				Props   json.RawMessage `json:"props"`
			} `json:"router"`
			Service  system.ServiceStatus `json:"service"`
			GPUs     []gpu.GPU            `json:"gpus"`
			GPUError string               `json:"gpu_error"`
		}
		if printed, err := getOrPrint(c, "/status", *asJSON, &st); printed || err != nil {
			return err
		}
		var props struct {
			BuildInfo    string `json:"build_info"`
			MaxInstances int    `json:"max_instances"`
		}
		json.Unmarshal(st.Router.Props, &props)
		health := "healthy"
		if !st.Router.Healthy {
			health = "DOWN: " + st.Router.Error
		}
		fmt.Printf("router   %s (%s, max %d loaded)\n", health, props.BuildInfo, props.MaxInstances)
		fmt.Printf("service  %s %s/%s since %s\n", st.Service.Unit, st.Service.ActiveState, st.Service.SubState, st.Service.Since)
		for _, g := range st.GPUs {
			fmt.Printf("gpu%d     %s  %5d/%d MiB  %3d%%  %d°C  %.0fW\n", g.Index, g.Name, g.MemUsedMiB, g.MemTotalMiB, g.UtilPct, g.TempC, g.PowerW)
		}
		if st.GPUError != "" {
			fmt.Println("gpu      ", st.GPUError)
		}
		return nil
	})
}

func cmdModels(args []string) error {
	fs, host := hostFlags("models")
	asJSON := jsonFlag(fs)
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	return withClient(*host, func(c *apiClient) error {
		var r struct {
			Data []llamacpp.Model `json:"data"`
		}
		if printed, err := getOrPrint(c, "/models", *asJSON, &r); printed || err != nil {
			return err
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tSTATUS\tSOURCE\tCTX")
		for _, m := range r.Data {
			ctx := ""
			for _, l := range strings.Split(m.Status.Preset, "\n") {
				if v, ok := strings.CutPrefix(l, "ctx-size = "); ok {
					ctx = v
				}
			}
			st := m.Status.Value
			if m.Status.Failed {
				st += " (failed)"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", m.ID, st, m.Source, ctx)
		}
		return tw.Flush()
	})
}

func cmdLoadUnload(action string, args []string) error {
	fs, host := hostFlags(action)
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return fmt.Errorf("usage: %s MODEL_ID [--host HOST]", action)
	}
	return withClient(*host, func(c *apiClient) error {
		if err := c.do("POST", "/models/"+action, map[string]string{"model": pos[0]}, nil); err != nil {
			return err
		}
		fmt.Printf("%s requested for %s\n", action, pos[0])
		return nil
	})
}

func cmdPresets(args []string) error {
	fs, host := hostFlags("presets")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	return withClient(*host, func(c *apiClient) error {
		var r struct {
			Path string `json:"path"`
			Text string `json:"text"`
		}
		if err := c.do("GET", "/presets", nil, &r); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "# %s\n", r.Path)
		fmt.Print(r.Text)
		return nil
	})
}

func cmdApply(args []string) error {
	fs, host := hostFlags("apply")
	yes := fs.Bool("yes", false, "apply without asking")
	check := fs.Bool("check", false, "only validate and preview")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: apply FILE [--host HOST] [--yes] [--check]")
	}
	text, err := os.ReadFile(pos[0])
	if err != nil {
		return err
	}
	return withClient(*host, func(c *apiClient) error {
		return applyText(c, string(text), "cli: "+filepath.Base(pos[0]), *yes, *check)
	})
}

// applyText previews a preset file (diff, issues, dry-run, model impact), asks, and applies it.
func applyText(c *apiClient, text, summary string, yes, check bool) error {
	var pr struct {
		Preview apply.Preview `json:"preview"`
		OK      bool          `json:"ok"`
	}
	if err := c.do("POST", "/presets/preview", map[string]string{"text": text}, &pr); err != nil {
		return err
	}
	p := pr.Preview
	if !p.Changed {
		fmt.Println("no changes")
	} else {
		fmt.Print(presets.Unified(p.Diff, 3))
	}
	for _, i := range p.Issues {
		fmt.Printf("%-7s [%s] %s\n", i.Severity, i.Section, i.Message)
	}
	if p.DryRunErr != "" {
		fmt.Println("dry-run:", p.DryRunErr)
	}
	for _, m := range p.Models {
		note := ""
		if m.Unloads {
			note = "  ← currently " + m.Status + ", will be UNLOADED"
		}
		fmt.Printf("model   %-8s %s%s\n", m.Change, m.ID, note)
	}
	if !pr.OK {
		return errors.New("preset is not valid; nothing applied")
	}
	if check || !p.Changed {
		return nil
	}
	if !yes && !confirm("Apply?") {
		return errors.New("cancelled")
	}
	var res apply.Result
	if err := c.do("POST", "/presets/apply", map[string]string{
		"text": text, "base_hash": p.BaseHash, "summary": summary,
	}, &res); err != nil {
		return err
	}
	for _, s := range res.Steps {
		mark := "✓"
		if !s.OK {
			mark = "✗"
		}
		fmt.Printf("%s %-9s %s (%dms)\n", mark, s.Name, s.Detail, s.Duration)
	}
	if !res.OK {
		return errors.New(res.Error)
	}
	return nil
}

func cmdLogs(args []string) error {
	fs, host := hostFlags("logs")
	n := fs.Int("n", 100, "number of lines")
	follow := fs.Bool("f", false, "follow")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	return withClient(*host, func(c *apiClient) error {
		if !*follow {
			var r struct {
				Lines []string `json:"lines"`
			}
			if err := c.do("GET", fmt.Sprintf("/logs?n=%d", *n), nil, &r); err != nil {
				return err
			}
			for _, l := range r.Lines {
				fmt.Println(l)
			}
			return nil
		}
		resp, err := c.http.Get(fmt.Sprintf("http://llamira/api/logs?n=%d&follow=1", *n))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			if d, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				var line string
				json.Unmarshal([]byte(d), &line)
				fmt.Println(line)
			}
		}
		return sc.Err()
	})
}

func cmdRestart(args []string) error {
	fs, host := hostFlags("restart")
	yes := fs.Bool("yes", false, "do not ask")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	if !*yes && !confirm("Restart llama-server? Loaded models are unloaded and in-flight requests fail.") {
		return errors.New("cancelled")
	}
	return withClient(*host, func(c *apiClient) error {
		t := time.Now()
		if err := c.do("POST", "/service/restart", nil, nil); err != nil {
			return err
		}
		fmt.Printf("router restarted and healthy (%.1fs)\n", time.Since(t).Seconds())
		return nil
	})
}
