package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"llamira/internal/bench"
	"llamira/internal/doctor"
	"llamira/internal/llamacpp"
)

// jsonFlag adds --json to a command: print the API response unchanged.
func jsonFlag(fs *flag.FlagSet) *bool {
	return fs.Bool("json", false, "print the API response as JSON")
}

// getOrPrint fetches path into out, or prints the raw JSON when asJSON is set (and returns
// printed = true).
func getOrPrint(c *apiClient, path string, asJSON bool, out any) (printed bool, err error) {
	if asJSON {
		var raw json.RawMessage
		if err := c.do("GET", path, nil, &raw); err != nil {
			return false, err
		}
		return true, printJSON(raw)
	}
	return false, c.do("GET", path, nil, out)
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func cmdDoctor(args []string) error {
	fs, host := hostFlags("doctor")
	asJSON := jsonFlag(fs)
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	return withClient(*host, func(c *apiClient) error {
		var r struct {
			Checks []doctor.Check `json:"checks"`
			Status string         `json:"status"`
		}
		if err := c.do("GET", "/doctor", nil, &r); err != nil {
			return err
		}
		if *asJSON {
			if err := printJSON(r); err != nil {
				return err
			}
		} else {
			mark := map[string]string{doctor.OK: "✓", doctor.Warn: "!", doctor.Fail: "✗"}
			for _, ch := range r.Checks {
				line := fmt.Sprintf("%s %-40s", mark[ch.Status], ch.Title)
				if ch.Detail != "" && (ch.Status != doctor.OK || len(ch.Detail) < 60) {
					line += " " + ch.Detail
				}
				fmt.Println(strings.TrimRight(line, " "))
				if ch.Fix != "" && ch.Status != doctor.OK {
					fmt.Println("    → " + ch.Fix)
				}
			}
		}
		fails := 0
		for _, ch := range r.Checks {
			if ch.Status == doctor.Fail {
				fails++
			}
		}
		if fails > 0 {
			return fmt.Errorf("%d check(s) failed", fails)
		}
		return nil
	})
}

func cmdDownload(args []string) error {
	fs, host := hostFlags("download")
	preset := fs.Bool("preset", false, "add a preset for the model when the download finishes")
	ctxSize := fs.Int("ctx", 0, "with --preset: ctx-size")
	device := fs.String("device", "", "with --preset: device (e.g. CUDA0)")
	embed := fs.Bool("embeddings", false, "with --preset: embedding model")
	wait := fs.Bool("wait", false, "wait until the download has finished")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: download ORG/REPO:QUANT [--preset [--ctx N] [--device CUDA0] [--embeddings]] [--wait] [--host HOST]")
	}
	id := pos[0]
	return withClient(*host, func(c *apiClient) error {
		body := map[string]any{"model": id}
		if *preset {
			body["preset"] = map[string]any{"ctx": *ctxSize, "device": *device, "embeddings": *embed}
		}
		if err := c.do("POST", "/models/download", body, nil); err != nil {
			return err
		}
		fmt.Printf("download started: %s\n", id)
		if !*wait {
			return nil
		}
		seen := false
		for start := time.Now(); ; time.Sleep(3 * time.Second) {
			var r struct {
				Data []llamacpp.Model `json:"data"`
			}
			if err := c.do("GET", "/models", nil, &r); err != nil {
				return err
			}
			var m *llamacpp.Model
			for i := range r.Data {
				if strings.EqualFold(r.Data[i].ID, id) {
					m = &r.Data[i]
				}
			}
			switch {
			case m != nil && m.Status.Value == "downloading":
				seen = true
				fmt.Fprintf(os.Stderr, "\rdownloading… %s", time.Since(start).Round(time.Second))
			case m != nil:
				fmt.Fprintln(os.Stderr)
				fmt.Printf("downloaded: %s\n", m.ID)
				return nil
			case seen || time.Since(start) > 2*time.Minute:
				fmt.Fprintln(os.Stderr)
				return fmt.Errorf("download of %s failed or was cancelled (see `llamira logs`)", id)
			}
		}
	})
}

func cmdDelete(args []string) error {
	fs, host := hostFlags("delete")
	yes := fs.Bool("yes", false, "do not ask")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: delete MODEL_ID [--yes] [--host HOST]")
	}
	if !*yes && !confirm(fmt.Sprintf("Delete %s from disk? This cannot be undone.", pos[0])) {
		return errors.New("cancelled")
	}
	return withClient(*host, func(c *apiClient) error {
		if err := c.do("POST", "/models/delete", map[string]string{"model": pos[0]}, nil); err != nil {
			return err
		}
		fmt.Printf("deleted %s\n", pos[0])
		return nil
	})
}

func cmdBench(args []string) error {
	fs, host := hostFlags("bench")
	asJSON := jsonFlag(fs)
	prompt := fs.Int("prompt", 512, "prompt tokens")
	gen := fs.Int("gen", 128, "tokens to generate")
	noRestore := fs.Bool("no-restore", false, "do not reload the previously loaded model afterwards")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: bench MODEL_ID [--prompt N] [--gen N] [--no-restore] [--json] [--host HOST]")
	}
	return withClient(*host, func(c *apiClient) error {
		fmt.Fprintf(os.Stderr, "benchmarking %s (%d prompt / %d gen tokens; loads the model if needed)…\n", pos[0], *prompt, *gen)
		var r struct {
			ID     int64        `json:"id"`
			Result bench.Result `json:"result"`
		}
		if err := c.do("POST", "/bench", map[string]any{"model": pos[0], "prompt_tokens": *prompt, "gen_tokens": *gen, "restore_loaded": !*noRestore}, &r); err != nil {
			return err
		}
		if *asJSON {
			return printJSON(r)
		}
		b := r.Result
		if b.Error != "" {
			return errors.New(b.Error)
		}
		if b.Build != "" {
			fmt.Printf("llama.cpp   %s\n", b.Build)
		}
		fmt.Printf("prompt      %6d tok  %8.1f t/s\n", b.PromptN, b.PromptTPS)
		fmt.Printf("generation  %6d tok  %8.1f t/s\n", b.GenN, b.GenTPS)
		fmt.Printf("ttft        %8.0f ms\n", b.TTFTMS)
		if b.LoadMS > 0 {
			fmt.Printf("load        %8.1f s\n", float64(b.LoadMS)/1000)
		}
		for _, g := range b.GPUs {
			fmt.Printf("gpu%d        peak %d MiB  max %d °C  avg %.0f W\n", g.Index, g.PeakMiB, g.MaxTempC, g.AvgPowerW)
		}
		return nil
	})
}

func cmdUsage(args []string) error {
	fs, host := hostFlags("usage")
	asJSON := jsonFlag(fs)
	days := fs.Int("days", 7, "period in days (1–90)")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	return withClient(*host, func(c *apiClient) error {
		var r struct {
			Models []struct {
				Model     string    `json:"model"`
				Requests  int64     `json:"requests"`
				PromptN   int64     `json:"prompt_tokens"`
				GenN      int64     `json:"gen_tokens"`
				PromptTPS float64   `json:"prompt_tps"`
				GenTPS    float64   `json:"gen_tps"`
				LastUsed  time.Time `json:"last_used"`
			} `json:"models"`
		}
		if printed, err := getOrPrint(c, fmt.Sprintf("/usage?days=%d", *days), *asJSON, &r); printed || err != nil {
			return err
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', tabwriter.AlignRight)
		fmt.Fprintln(tw, "MODEL\tREQUESTS\tPROMPT TOK\tGEN TOK\tPP T/S\tTG T/S\tLAST USED\t")
		for _, m := range r.Models {
			fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%.0f\t%.1f\t%s\t\n", m.Model, m.Requests, m.PromptN, m.GenN, m.PromptTPS, m.GenTPS, m.LastUsed.Local().Format("2006-01-02 15:04"))
		}
		return tw.Flush()
	})
}

func cmdPower(args []string) error {
	fs, host := hostFlags("power")
	asJSON := jsonFlag(fs)
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 && len(pos) != 2 {
		return errors.New("usage: power [GPU WATTS|GPU default] [--json] [--host HOST]")
	}
	return withClient(*host, func(c *apiClient) error {
		if len(pos) == 2 {
			idx, err := strconv.Atoi(pos[0])
			if err != nil {
				return fmt.Errorf("GPU must be an index, got %q", pos[0])
			}
			watts := 0.0 // default
			if pos[1] != "default" {
				if watts, err = strconv.ParseFloat(strings.TrimSuffix(pos[1], "W"), 64); err != nil || watts <= 0 {
					return fmt.Errorf("WATTS must be a number or default, got %q", pos[1])
				}
			}
			var out struct {
				Output string `json:"output"`
			}
			if err := c.do("POST", "/gpu/power", map[string]any{"index": idx, "watts": watts}, &out); err != nil {
				return err
			}
			fmt.Print(out.Output)
			return nil
		}
		var ps []struct {
			Index     int     `json:"index"`
			Limit     float64 `json:"limit_w"`
			Default   float64 `json:"default_w"`
			Min       float64 `json:"min_w"`
			Max       float64 `json:"max_w"`
			Persisted bool    `json:"persisted"`
		}
		if printed, err := getOrPrint(c, "/gpu/power", *asJSON, &ps); printed || err != nil {
			return err
		}
		for _, p := range ps {
			note := ""
			if p.Persisted {
				note = "  (kept after reboot)"
			}
			fmt.Printf("gpu%d  %.0f W  (default %.0f, range %.0f–%.0f)%s\n", p.Index, p.Limit, p.Default, p.Min, p.Max, note)
		}
		return nil
	})
}

func cmdAliases(args []string) error {
	fs, host := hostFlags("aliases")
	asJSON := jsonFlag(fs)
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	return withClient(*host, func(c *apiClient) error {
		var r struct {
			Data []llamacpp.Model `json:"data"`
		}
		if err := c.do("GET", "/models", nil, &r); err != nil {
			return err
		}
		out := map[string]string{}
		for _, m := range r.Data {
			for _, a := range m.Aliases {
				out[a] = m.ID
			}
		}
		if *asJSON {
			return printJSON(out)
		}
		names := make([]string, 0, len(out))
		for a := range out {
			names = append(names, a)
		}
		sort.Strings(names)
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "ALIAS\tMODEL")
		for _, a := range names {
			fmt.Fprintf(tw, "%s\t%s\n", a, out[a])
		}
		return tw.Flush()
	})
}

func cmdExport(args []string) error {
	fs, host := hostFlags("export")
	secrets := fs.Bool("secrets", false, "include tokens (alert channels, config)")
	outFile := fs.String("o", "", "write to FILE instead of stdout")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	return withClient(*host, func(c *apiClient) error {
		path := "/export"
		if *secrets {
			path += "?secrets=1"
		}
		var raw json.RawMessage
		if err := c.do("GET", path, nil, &raw); err != nil {
			return err
		}
		if *outFile == "" {
			return printJSON(raw)
		}
		f, err := os.OpenFile(*outFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return err
		}
		enc := json.NewEncoder(f)
		enc.SetIndent("", "  ")
		if err := enc.Encode(raw); err != nil {
			f.Close()
			return err
		}
		fmt.Fprintf(os.Stderr, "wrote %s\n", *outFile)
		return f.Close()
	})
}

func cmdImport(args []string) error {
	fs, host := hostFlags("import")
	parts := fs.String("parts", "presets,schedules,alerts,power", "what to import (comma-separated)")
	yes := fs.Bool("yes", false, "do not ask")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: import FILE [--parts presets,schedules,alerts,power] [--yes] [--host HOST]")
	}
	data, err := os.ReadFile(pos[0])
	if err != nil {
		return err
	}
	var b struct {
		Format      int               `json:"llamira_export"`
		Host        string            `json:"host"`
		Time        time.Time         `json:"time"`
		Presets     string            `json:"presets"`
		Schedules   []json.RawMessage `json:"schedules"`
		Alerts      json.RawMessage   `json:"alerts"`
		PowerLimits map[string]any    `json:"power_limits"`
	}
	if err := json.Unmarshal(data, &b); err != nil || b.Format != 1 {
		return fmt.Errorf("%s is not a llamira export", pos[0])
	}
	want := map[string]bool{}
	for _, p := range strings.Split(*parts, ",") {
		want[strings.TrimSpace(p)] = true
	}
	fmt.Printf("export of %s from %s\n", b.Host, b.Time.Local().Format("2006-01-02 15:04"))
	return withClient(*host, func(c *apiClient) error {
		var rest []string
		for _, p := range []string{"schedules", "alerts", "power"} {
			if want[p] {
				rest = append(rest, p)
			}
		}
		if len(rest) > 0 {
			fmt.Printf("will replace: %s (%d schedules, %d power limits)\n", strings.Join(rest, ", "), len(b.Schedules), len(b.PowerLimits))
			if !*yes && !confirm("Import these?") {
				return errors.New("cancelled")
			}
			var res struct {
				Imported []string `json:"imported"`
				Errors   []string `json:"errors"`
			}
			if err := c.do("POST", "/import", map[string]any{"bundle": json.RawMessage(data), "parts": rest}, &res); err != nil {
				return err
			}
			for _, d := range res.Imported {
				fmt.Println("✓", d)
			}
			for _, e := range res.Errors {
				fmt.Println("✗", e)
			}
		}
		if want["presets"] && b.Presets != "" {
			fmt.Println("presets:")
			return applyText(c, b.Presets, "import from "+b.Host, *yes, false)
		}
		return nil
	})
}
