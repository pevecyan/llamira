package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"llamira/internal/config"
	"llamira/internal/install"
	"llamira/internal/llamacpp"
	"llamira/internal/remote"
	"llamira/internal/system"
	"llamira/web"
)

func cmdInstall(args []string) error {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	cfgPath := fs.String("config", config.DefaultServerConfig, "config file")
	adopt := fs.Bool("adopt", false, "move per-model llama-server flags into the preset file")
	yes := fs.Bool("yes", false, "do not ask for confirmation")
	listen := fs.String("listen", "", "UI address, e.g. 0.0.0.0:7070 for the LAN (generates an auth token)")
	auth := fs.String("auth", "", "LAN access: token (default) or none (no authentication)")
	tlsMode := fs.String("tls", "", "LAN access over HTTPS: self-signed, or off")
	readToken := fs.Bool("read-token", false, "also create a read-only token (dashboards)")
	from := fs.String("llama-cpp", "prebuilt", "fresh install: prebuilt (upstream release) or source (cmake build)")
	ref := fs.String("llama-ref", "", "fresh install: llama.cpp release tag (default: latest)")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	if *from != "prebuilt" && *from != "source" {
		return errors.New("--llama-cpp must be prebuilt or source")
	}
	return install.Run(context.Background(), install.Options{
		ConfigPath: *cfgPath, Adopt: *adopt, Yes: *yes, Listen: *listen, Auth: *auth, TLS: *tlsMode, ReadToken: *readToken,
		Out: os.Stdout, Confirm: confirm,
		LlamaFrom: *from, LlamaRef: *ref,
	})
}

func loadClientConfig() (config.Client, error) { return config.LoadClient(config.ClientPath()) }

func cmdHosts(args []string) error {
	c, err := loadClientConfig()
	if err != nil {
		return err
	}
	if len(args) == 0 {
		if len(c.Hosts) == 0 {
			fmt.Println("no hosts configured; add one with: llamira hosts add NAME user@host")
			return nil
		}
		for _, n := range c.Names() {
			mark := " "
			if n == c.Default {
				mark = "*"
			}
			fmt.Printf("%s %-16s %s\n", mark, n, c.Hosts[n].SSH)
		}
		return nil
	}
	switch args[0] {
	case "add":
		if len(args) != 3 {
			return errors.New("usage: hosts add NAME user@host[:port]")
		}
		c.Hosts[args[1]] = config.Host{SSH: args[2]}
		if c.Default == "" {
			c.Default = args[1]
		}
	case "rm":
		if len(args) != 2 {
			return errors.New("usage: hosts rm NAME")
		}
		delete(c.Hosts, args[1])
		if c.Default == args[1] {
			c.Default = ""
		}
	case "default":
		if len(args) != 2 || c.Hosts[args[1]].SSH == "" {
			return errors.New("usage: hosts default NAME (an existing host)")
		}
		c.Default = args[1]
	default:
		return fmt.Errorf("unknown hosts subcommand %q", args[0])
	}
	return config.SaveClient(config.ClientPath(), c)
}

type hostEntry struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	conn  *remote.Conn
}

func cmdConnect(args []string) error {
	fs := flag.NewFlagSet("connect", flag.ExitOnError)
	listen := fs.String("listen", "127.0.0.1:7070", "local address for the UI")
	noBrowser := fs.Bool("no-browser", false, "do not open a browser")
	targets, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	cc, err := loadClientConfig()
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		targets = cc.Names()
		if len(targets) == 0 {
			return errors.New("no hosts given and none in " + config.ClientPath())
		}
	}
	var hosts []*hostEntry
	seen := map[string]bool{}
	for _, t := range targets {
		name, h, err := cc.Resolve(t)
		if err != nil {
			return err
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		hosts = append(hosts, &hostEntry{Name: name, Label: h.SSH, conn: remote.NewConn(name, h)})
	}
	sort.Slice(hosts, func(i, j int) bool { return hosts[i].Name < hosts[j].Name })

	// Connect up front so password/host-key prompts happen before the server starts.
	for _, h := range hosts {
		fmt.Fprintf(os.Stderr, "connecting to %s (%s)... ", h.Name, h.Label)
		if _, err := h.conn.Client(); err != nil {
			fmt.Fprintln(os.Stderr, "failed:", err)
			continue
		}
		hc := &http.Client{Transport: h.conn.Transport(), Timeout: 10 * time.Second}
		resp, err := hc.Get("http://llamira/api/info")
		if err != nil {
			fmt.Fprintln(os.Stderr, "ssh ok, but llamira is not reachable:", err)
			continue
		}
		resp.Body.Close()
		fmt.Fprintln(os.Stderr, "ok")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/hosts", func(w http.ResponseWriter, r *http.Request) {
		type row struct {
			Name      string `json:"name"`
			Label     string `json:"label"`
			Connected bool   `json:"connected"`
			Error     string `json:"error,omitempty"`
		}
		rows := make([]row, len(hosts))
		var wg sync.WaitGroup
		for i, h := range hosts {
			wg.Add(1)
			go func() {
				defer wg.Done()
				rows[i] = row{Name: h.Name, Label: h.Label}
				ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
				defer cancel()
				c, err := h.conn.DialSocket(ctx)
				if err != nil {
					rows[i].Error = err.Error()
					return
				}
				c.Close()
				rows[i].Connected = true
			}()
		}
		wg.Wait()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(rows)
	})
	for _, h := range hosts {
		prefix := "/api/hosts/" + h.Name
		mux.Handle(prefix+"/", h.conn.Proxy(prefix))
	}
	mux.Handle("/", web.Handler())

	l, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	url := "http://" + l.Addr().String() + "/"
	fmt.Fprintf(os.Stderr, "\nllamira UI: %s  (Ctrl+C to stop)\n", url)
	if !*noBrowser {
		openBrowser(url)
	}
	srv := &http.Server{Handler: mux}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		srv.Shutdown(sctx)
	}()
	err = srv.Serve(l)
	for _, h := range hosts {
		h.conn.Close()
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}

func cmdDeploy(args []string) error {
	fs := flag.NewFlagSet("deploy", flag.ExitOnError)
	adopt := fs.Bool("adopt", false, "also adopt the llama-server router (see install --adopt)")
	yes := fs.Bool("yes", false, "do not ask for confirmation")
	binary := fs.String("binary", "", "linux binary to upload (default: this executable)")
	listen := fs.String("listen", "", "UI address on the host, e.g. 0.0.0.0:7070 for the LAN")
	auth := fs.String("auth", "", "LAN access: token (default) or none (no authentication)")
	tlsMode := fs.String("tls", "", "LAN access over HTTPS: self-signed, or off")
	readToken := fs.Bool("read-token", false, "also create a read-only token (dashboards)")
	from := fs.String("llama-cpp", "", "fresh hosts: prebuilt (default) or source")
	llamaRef := fs.String("llama-ref", "", "fresh hosts: llama.cpp release tag (default: latest)")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: deploy HOST [--adopt] [--yes]")
	}
	cc, err := loadClientConfig()
	if err != nil {
		return err
	}
	name, h, err := cc.Resolve(pos[0])
	if err != nil {
		return err
	}
	conn := remote.NewConn(name, h)
	defer conn.Close()

	arch, err := conn.Run("uname -m", nil)
	if err != nil {
		return fmt.Errorf("uname: %v: %s", err, arch)
	}
	arch = strings.TrimSpace(arch)
	want := map[string]string{"x86_64": "amd64", "aarch64": "arm64"}[arch]
	if *binary == "" {
		if runtime.GOOS != "linux" || runtime.GOARCH != want {
			return fmt.Errorf("remote is linux/%s; pass --binary with a linux/%s build", arch, want)
		}
		if *binary, err = os.Executable(); err != nil {
			return err
		}
	}
	f, err := os.Open(*binary)
	if err != nil {
		return err
	}
	defer f.Close()
	who, _ := conn.Run("id -u", nil)
	sudo := ""
	if strings.TrimSpace(who) != "0" {
		sudo = "sudo "
	}
	fmt.Fprintf(os.Stderr, "uploading %s to %s...\n", *binary, name)
	tmp := "/tmp/llamira.upload"
	if out, err := conn.Run("cat > "+tmp+" && chmod 755 "+tmp, f); err != nil {
		return fmt.Errorf("upload: %v: %s", err, out)
	}
	cmd := sudo + tmp + " install"
	if *adopt {
		cmd += " --adopt"
	}
	if *yes {
		cmd += " --yes"
	}
	if *listen != "" {
		cmd += " --listen " + shellQuote(*listen)
	}
	if *auth != "" {
		cmd += " --auth " + shellQuote(*auth)
	}
	if *tlsMode != "" {
		cmd += " --tls " + shellQuote(*tlsMode)
	}
	if *readToken {
		cmd += " --read-token"
	}
	if *from != "" {
		cmd += " --llama-cpp " + shellQuote(*from)
	}
	if *llamaRef != "" {
		cmd += " --llama-ref " + shellQuote(*llamaRef)
	}
	cmd += "; rc=$?; rm -f " + tmp + "; exit $rc"
	if err := conn.RunStream(cmd, os.Stdin, os.Stdout); err != nil {
		return fmt.Errorf("remote install: %w", err)
	}
	if _, ok := cc.Hosts[name]; !ok {
		fmt.Fprintf(os.Stderr, "\ntip: llamira hosts add %s %s\n", name, h.SSH)
	}
	fmt.Fprintf(os.Stderr, "done. open the UI with: llamira connect %s\n", pos[0])
	return nil
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// cmdLlamacppBuild builds a llama.cpp version (run as root by `privileged build` inside a
// transient systemd unit; output goes to the journal).
func cmdLlamacppBuild(args []string) error {
	fs := flag.NewFlagSet("llamacpp-build", flag.ExitOnError)
	cfgPath := fs.String("config", config.DefaultServerConfig, "config file")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: llamacpp-build REF")
	}
	cfg, err := config.LoadServer(*cfgPath)
	if err != nil {
		return err
	}
	env := cfg.Router.Env
	if len(env) == 0 {
		env, _ = system.UnitEnv(context.Background(), cfg.Router.Service)
	}
	builds := cfg.Router.BuildsDir
	if builds == "" {
		builds = llamacpp.DefaultBuildsDir
	}
	return llamacpp.RunBuild(context.Background(), llamacpp.BuildOptions{
		SrcDir: cfg.Router.WorkDir, BuildsDir: builds, Ref: pos[0], Env: env,
		PresetFile: cfg.PresetFile, ModelsDir: cfg.Router.ModelsDir, Out: os.Stdout,
	})
}

// cmdLlamacppFetch downloads a prebuilt llama.cpp release (run as root by `privileged fetch`).
func cmdLlamacppFetch(args []string) error {
	fs := flag.NewFlagSet("llamacpp-fetch", flag.ExitOnError)
	cfgPath := fs.String("config", config.DefaultServerConfig, "config file")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: llamacpp-fetch REF")
	}
	cfg, err := config.LoadServer(*cfgPath)
	if err != nil {
		return err
	}
	env := cfg.Router.Env
	if len(env) == 0 {
		env, _ = system.UnitEnv(context.Background(), cfg.Router.Service)
	}
	builds := cfg.Router.BuildsDir
	if builds == "" {
		builds = llamacpp.DefaultBuildsDir
	}
	h := llamacpp.DetectHost(context.Background(), "/")
	fmt.Printf("==> host %s %s, prebuilt flavour %s\n", h.OS, h.Arch, h.Flavor)
	return llamacpp.FetchPrebuilt(context.Background(), llamacpp.FetchOptions{
		BuildsDir: builds, Ref: pos[0], Flavor: h.Flavor, Arch: h.Arch, WorkDir: cfg.Router.WorkDir, Env: env,
		PresetFile: cfg.PresetFile, ModelsDir: cfg.Router.ModelsDir, Out: os.Stdout,
	})
}

func cmdUninstall(args []string) error {
	fs := flag.NewFlagSet("uninstall", flag.ExitOnError)
	cfgPath := fs.String("config", config.DefaultServerConfig, "config file")
	purge := fs.Bool("purge-models", false, "also delete the downloaded models")
	pkgs := fs.Bool("remove-packages", false, "also remove packages the install added")
	yes := fs.Bool("yes", false, "do not ask for confirmation")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	return install.Uninstall(context.Background(), *cfgPath, *purge, *pkgs, install.Options{Yes: *yes, Out: os.Stdout, Confirm: confirm})
}

func cmdDetect(args []string) error {
	h := llamacpp.DetectHost(context.Background(), "/")
	b, _ := json.MarshalIndent(h, "", "  ")
	fmt.Println(string(b))
	return nil
}
