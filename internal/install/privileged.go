package install

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"llamira/internal/config"
	"llamira/internal/llamacpp"
	"llamira/internal/system"
)

// Privileged runs one root-only operation for the service user. It is the only command the
// sudoers rule allows besides restarting the router, so every argument is validated here.
//
//	privileged build REF          start a background build (transient systemd unit, low priority)
//	privileged fetch REF          download the prebuilt release REF for this host (background)
//	privileged cancel-build REF   stop a running build
//	privileged remove-build REF   delete a build (not the active one)
//	privileged switch REF         point the router at a build ("original" = source tree build)
//	privileged power IDX:WATTS    set (and persist) a GPU power limit; IDX:default resets it
//	privileged power-apply all    reapply persisted power limits (boot unit)
//	privileged power-temp IDX:W   set a power limit without persisting it (power sweep)
//	privileged router-set K=V     models-max, sleep-idle-seconds, models-autoload, api-key=on|off|rotate
func Privileged(ctx context.Context, cfgPath string, args []string, out io.Writer) error {
	if os.Geteuid() != 0 {
		return errors.New("privileged operations must run as root (via sudo)")
	}
	if len(args) != 2 {
		return errors.New("usage: privileged build|cancel-build|remove-build|switch REF")
	}
	op, ref := args[0], args[1]
	switch op {
	case "power":
		return setPower(ctx, ref, out)
	case "power-apply":
		return applyPower(ctx, out)
	case "power-temp":
		return setPowerTemporary(ctx, ref, out)
	case "router-set":
		cfg, err := config.LoadServer(cfgPath)
		if err != nil {
			return err
		}
		return routerSet(ctx, cfg, ref, out)
	}
	if ref != "original" && !llamacpp.ValidRef(ref) {
		return fmt.Errorf("invalid ref %q", ref)
	}
	cfg, err := config.LoadServer(cfgPath)
	if err != nil {
		return err
	}
	src, builds := cfg.Router.WorkDir, buildsDir(cfg)
	switch op {
	case "build":
		if ref == "original" {
			return errors.New("cannot rebuild the original build")
		}
		unit := llamacpp.BuildUnit(ref)
		if system.IsActive(ctx, unit) {
			return fmt.Errorf("a build of %s is already running", ref)
		}
		_ = run("systemctl", "reset-failed", unit)
		return run("systemd-run", "--unit="+unit, "--description=llamira: build llama.cpp "+ref,
			"--nice=19", "--property=CPUWeight=10", "--property=IOWeight=10",
			BinPath, "llamacpp-build", "--config", cfgPath, ref)
	case "fetch":
		unit := llamacpp.BuildUnit(ref)
		if system.IsActive(ctx, unit) {
			return fmt.Errorf("a build of %s is already running", ref)
		}
		_ = run("systemctl", "reset-failed", unit)
		return run("systemd-run", "--unit="+unit, "--description=llamira: download llama.cpp "+ref,
			"--nice=10", BinPath, "llamacpp-fetch", "--config", cfgPath, ref)
	case "cancel-build":
		return run("systemctl", "stop", llamacpp.BuildUnit(ref))
	case "remove-build":
		if ref == "original" {
			return errors.New("the original build cannot be removed")
		}
		bin, _ := llamacpp.BinaryFor(src, builds, ref)
		if active, _ := ActiveBinary(ctx, cfg.Router.Service); bin != "" && active == bin {
			return errors.New("this build is in use; switch to another one first")
		}
		return llamacpp.RemoveBuild(src, builds, ref)
	case "switch":
		return Switch(ctx, cfg, ref, out)
	}
	return fmt.Errorf("unknown operation %q", op)
}

func buildsDir(cfg config.Server) string {
	if cfg.Router.BuildsDir != "" {
		return cfg.Router.BuildsDir
	}
	return llamacpp.DefaultBuildsDir
}

// ActiveBinary returns the llama-server binary the router service runs (argv[0] of ExecStart).
func ActiveBinary(ctx context.Context, unit string) (string, error) {
	props, err := system.Show(ctx, unit, "ExecStart")
	if err != nil {
		return "", err
	}
	argv := strings.Fields(execArgv(props["ExecStart"]))
	if len(argv) == 0 {
		return "", errors.New("cannot read ExecStart")
	}
	return argv[0], nil
}

// Switch points the router's drop-in at another llama-server build.
func Switch(ctx context.Context, cfg config.Server, ref string, out io.Writer) error {
	bin, err := llamacpp.BinaryFor(cfg.Router.WorkDir, buildsDir(cfg), ref)
	if err != nil {
		return err
	}
	if _, err := os.Stat(bin); err != nil {
		return fmt.Errorf("%s: %w", bin, err)
	}
	return changeExecStart(ctx, cfg, "switch to "+ref, func(argv []string) ([]string, error) {
		argv[0] = bin
		return argv, nil
	}, nil, out)
}

// changeExecStart rewrites the router drop-in's ExecStart with mutate, restarts the router and
// verifies health and the model list; on failure the previous drop-in (and anything undo
// reverts) is restored.
func changeExecStart(ctx context.Context, cfg config.Server, what string, mutate func([]string) ([]string, error), undo func(), out io.Writer) error {
	logf := func(format string, a ...any) { fmt.Fprintf(out, format+"\n", a...) }
	unit := cfg.Router.Service
	props, err := system.Show(ctx, unit, "FragmentPath")
	if err != nil {
		return err
	}
	dropIn := filepath.Join(props["FragmentPath"]+".d", DropInName)
	prev, err := os.ReadFile(dropIn)
	if err != nil {
		return fmt.Errorf("router not adopted (no %s): run install --adopt first", dropIn)
	}
	var lines []string
	replaced := false
	for _, l := range strings.Split(string(prev), "\n") {
		if v, ok := strings.CutPrefix(l, "ExecStart="); ok && strings.TrimSpace(v) != "" {
			argv, err := mutate(strings.Fields(v))
			if err != nil {
				return err
			}
			l = "ExecStart=" + strings.Join(argv, " ")
			replaced = true
		}
		lines = append(lines, l)
	}
	if !replaced {
		return fmt.Errorf("no ExecStart in %s", dropIn)
	}
	router := routerClient(cfg)
	before := modelIDs(ctx, router)

	logf("%s: restarting %s", what, unit)
	if err := os.WriteFile(dropIn, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		return err
	}
	revert := func(cause error) error {
		logf("FAILED: %v, restoring the previous configuration", cause)
		_ = os.WriteFile(dropIn, prev, 0o644)
		if undo != nil {
			undo()
		}
		_ = run("systemctl", "daemon-reload")
		_ = run("systemctl", "restart", unit)
		return fmt.Errorf("%s failed, previous configuration restored: %w", what, cause)
	}
	if err := run("systemctl", "daemon-reload"); err != nil {
		return revert(err)
	}
	if err := run("systemctl", "restart", unit); err != nil {
		return revert(err)
	}
	deadline := time.Now().Add(2 * time.Minute)
	for router.Health(ctx) != nil {
		if time.Now().After(deadline) {
			return revert(errors.New("router not healthy after restart"))
		}
		time.Sleep(500 * time.Millisecond)
	}
	after := modelIDs(ctx, router)
	for _, id := range before {
		if !slices.Contains(after, id) {
			return revert(fmt.Errorf("model %s missing after the restart", id))
		}
	}
	logf("router healthy, %d models", len(after))
	return nil
}

func routerClient(cfg config.Server) *llamacpp.Client {
	c := llamacpp.NewClient(cfg.Router.URL, cfg.Router.APIKey)
	c.KeyFile = cfg.Router.APIKeyFile
	return c
}
