package install

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"llamira/internal/config"
	"llamira/internal/system"
)

// RouterSettings are the router-level flags managed from the UI.
type RouterSettings struct {
	ModelsMax        int    `json:"models_max"`
	SleepIdleSeconds int    `json:"sleep_idle_seconds"` // -1 = disabled
	ModelsAutoload   bool   `json:"models_autoload"`
	APIKey           bool   `json:"api_key"`
	Host             string `json:"host"`
	Port             string `json:"port"`
	ExecStart        string `json:"exec_start"`
}

// ReadRouterSettings parses the router's current ExecStart.
func ReadRouterSettings(ctx context.Context, unit string) (RouterSettings, error) {
	props, err := system.Show(ctx, unit, "ExecStart")
	if err != nil {
		return RouterSettings{}, err
	}
	argv := strings.Fields(execArgv(props["ExecStart"]))
	rs := RouterSettings{ModelsMax: 4, SleepIdleSeconds: -1, ModelsAutoload: true, ExecStart: strings.Join(argv, " ")}
	for i := 1; i < len(argv); i++ {
		name, val, hasEq := strings.Cut(strings.TrimLeft(argv[i], "-"), "=")
		next := func() string {
			if hasEq {
				return val
			}
			if i+1 < len(argv) {
				i++
				return argv[i]
			}
			return ""
		}
		switch name {
		case "models-max":
			rs.ModelsMax, _ = strconv.Atoi(next())
		case "sleep-idle-seconds":
			rs.SleepIdleSeconds, _ = strconv.Atoi(next())
		case "no-models-autoload":
			rs.ModelsAutoload = false
		case "models-autoload":
			rs.ModelsAutoload = true
		case "api-key", "api-key-file":
			rs.APIKey = true
			next()
		case "host":
			rs.Host = next()
		case "port":
			rs.Port = next()
		}
	}
	return rs, nil
}

// removeArg drops a flag (and its value) from argv.
func removeArg(argv []string, names []string, hasValue bool) []string {
	out := []string{argv[0]}
	for i := 1; i < len(argv); i++ {
		name, _, hasEq := strings.Cut(strings.TrimLeft(argv[i], "-"), "=")
		if strings.HasPrefix(argv[i], "-") && contains(names, name) {
			if hasValue && !hasEq {
				i++
			}
			continue
		}
		out = append(out, argv[i])
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// routerSet changes one router setting ("KEY=VALUE") and restarts the router (with rollback).
func routerSet(ctx context.Context, cfg config.Server, spec string, out io.Writer) error {
	key, val, ok := strings.Cut(spec, "=")
	if !ok {
		return fmt.Errorf("invalid setting %q (want KEY=VALUE)", spec)
	}
	num := func(lo, hi int) (int, error) {
		n, err := strconv.Atoi(val)
		if err != nil || n < lo || n > hi {
			return 0, fmt.Errorf("%s must be a number between %d and %d", key, lo, hi)
		}
		return n, nil
	}
	var mutate func([]string) ([]string, error)
	var undo func()
	switch key {
	case "models-max":
		n, err := num(0, 64)
		if err != nil {
			return err
		}
		mutate = func(a []string) ([]string, error) {
			return append(removeArg(a, []string{"models-max"}, true), "--models-max", strconv.Itoa(n)), nil
		}
	case "sleep-idle-seconds":
		n, err := num(-1, 7*24*3600)
		if err != nil {
			return err
		}
		mutate = func(a []string) ([]string, error) {
			a = removeArg(a, []string{"sleep-idle-seconds"}, true)
			if n > 0 {
				a = append(a, "--sleep-idle-seconds", strconv.Itoa(n))
			}
			return a, nil
		}
	case "models-autoload":
		if val != "true" && val != "false" {
			return fmt.Errorf("models-autoload must be true or false")
		}
		mutate = func(a []string) ([]string, error) {
			a = removeArg(a, []string{"models-autoload", "no-models-autoload"}, false)
			if val == "false" {
				a = append(a, "--no-models-autoload")
			}
			return a, nil
		}
	case "api-key":
		file := cfg.Router.APIKeyFile
		if file == "" {
			return fmt.Errorf("router.api_key_file is not configured")
		}
		prevKey, prevErr := os.ReadFile(file)
		switch val {
		case "on", "rotate":
			if val == "on" && prevErr == nil && len(strings.TrimSpace(string(prevKey))) > 0 {
				break // keep an existing key when re-enabling
			}
			b := make([]byte, 24)
			if _, err := rand.Read(b); err != nil {
				return err
			}
			if err := os.WriteFile(file, []byte("sk-llama-"+hex.EncodeToString(b)+"\n"), 0o640); err != nil {
				return err
			}
			_ = run("chgrp", User, file)
			_ = os.Chmod(file, 0o640)
		case "off":
		default:
			return fmt.Errorf("api-key must be on, off or rotate")
		}
		undo = func() {
			if prevErr == nil {
				_ = os.WriteFile(file, prevKey, 0o640)
			}
		}
		mutate = func(a []string) ([]string, error) {
			a = removeArg(a, []string{"api-key", "api-key-file"}, true)
			if val != "off" {
				a = append(a, "--api-key-file", file)
			}
			return a, nil
		}
	default:
		return fmt.Errorf("unknown router setting %q", key)
	}
	return changeExecStart(ctx, cfg, "set "+spec, mutate, undo, out)
}
