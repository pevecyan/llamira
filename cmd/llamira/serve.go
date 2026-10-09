package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"llamira/internal/alerts"
	"llamira/internal/api"
	"llamira/internal/apply"
	"llamira/internal/bench"
	"llamira/internal/config"
	"llamira/internal/hf"
	"llamira/internal/hoststat"
	"llamira/internal/install"
	"llamira/internal/llamacpp"
	"llamira/internal/metrics"
	"llamira/internal/recommend"
	"llamira/internal/schedule"
	"llamira/internal/store"
	"llamira/internal/system"
	"llamira/internal/tlscert"
	usagestats "llamira/internal/usage"
	"llamira/web"
)

// LocalHost is the host name the UI uses when talking to `serve` directly.
const LocalHost = "local"

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	cfgPath := fs.String("config", config.DefaultServerConfig, "config file")
	listen := fs.String("listen", "", "override listen address")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	cfg, err := config.LoadServer(*cfgPath)
	if err != nil {
		return err
	}
	if *listen != "" {
		cfg.Listen = *listen
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	if cfg.Listen != "" && !config.IsLoopback(cfg.Listen) && cfg.AuthToken == "" && cfg.PasswordHash == "" && !cfg.AllowUnauthenticated {
		return fmt.Errorf("listen %s is not loopback: set auth_token or a password (`llamira password`), or allow_unauthenticated: true, in %s", cfg.Listen, *cfgPath)
	}
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return err
	}
	st, err := store.Open(filepath.Join(cfg.DataDir, "llamira.db"))
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer st.Close()

	router := llamacpp.NewClient(cfg.Router.URL, cfg.Router.APIKey)
	router.KeyFile = cfg.Router.APIKeyFile
	activeBinary := activeBinaryResolver(cfg)
	catalog := &llamacpp.CatalogCache{Binary: cfg.Router.Binary, Resolve: activeBinary}
	am := &apply.Manager{
		PresetPath: cfg.PresetFile,
		BackupDir:  filepath.Join(cfg.DataDir, "backups"),
		Keep:       cfg.Backups,
		ModelsDir:  cfg.Router.ModelsDir,
		Service:    cfg.Router.Service,
		Router:     router,
		Catalog:    catalog,
		DryRunOpts: llamacpp.DryRunOptions{
			Binary: cfg.Router.Binary, WorkDir: cfg.Router.WorkDir,
			ModelsDir: cfg.Router.ModelsDir, Env: cfg.Router.Env,
		},
		EnvFromSystemd: len(cfg.Router.Env) == 0,
		BinaryFunc:     activeBinary,
		Store:          st,
	}
	benchRunner := &bench.Runner{Router: router}
	sweeper := &bench.Sweeper{
		Runner: benchRunner, Store: st,
		SetLimit: func(ctx context.Context, gpu int, watts float64) error {
			_, err := system.Privileged(ctx, install.BinPath, "power-temp", fmt.Sprintf("%d:%.0f", gpu, watts))
			return err
		},
		Build: func(ctx context.Context) string {
			if c, err := catalog.Get(ctx); err == nil {
				return c.Version
			}
			return ""
		},
	}
	// an interrupted sweep (crash, restart) must not leave GPUs at a test power limit
	go sweeper.RecoverLimits(context.Background())
	srv := &api.Server{Cfg: cfg, Version: version, Router: router, Catalog: catalog, Apply: am, Store: st,
		HF: hf.NewClient(cfg.HFToken), Bench: benchRunner, ActiveBinary: activeBinary,
		Recommend:  &recommend.Engine{HF: hf.NewClient(cfg.HFToken), CatalogURL: cfg.CatalogURL},
		Sweeper:    sweeper,
		ConfigPath: *cfgPath, Log: log}
	hostStats := &hoststat.Sampler{UnitName: cfg.Router.Service, Unit: api.UnitInfo(cfg.Router.Service)}
	srv.Host = hostStats
	benchRunner.Host = hostStats.Latest
	apiH := srv.Handler()
	prom := metrics.New(router, cfg.Router.ModelsDir)
	prom.Host = hostStats.Latest

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/hosts", func(w http.ResponseWriter, r *http.Request) {
		host, _ := os.Hostname()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `[{"name":%q,"label":%q,"connected":true}]`, LocalHost, host)
	})
	mux.Handle("/api/hosts/"+LocalHost+"/", http.StripPrefix("/api/hosts/"+LocalHost, rewriteAPI(apiH)))
	mux.Handle("/api/", apiH)
	mux.Handle("GET /metrics", prom)
	mux.Handle("/", web.Handler())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// usage statistics: follow the router journal (backfill 30 days on first start)
	_ = st.ResetUsage("3") // bump to re-parse the journal after parser changes
	_ = st.PruneRequests(90 * 24 * time.Hour)
	go usagestats.Follow(ctx, cfg.Router.Service, 30*24*time.Hour, observingSink{st, prom}, log)
	go hostStats.Run(ctx, 2*time.Second)
	monitor := &alerts.Monitor{Router: router, Store: st, ModelsDir: cfg.Router.ModelsDir, Log: log, Host: hostStats.Latest,
		Release: func(ctx context.Context) (string, string, bool) {
			u := srv.CheckUpdate(ctx)
			latest, current := u.LatestVersion+" ("+u.Latest+")", u.Current
			if u.CurrentVersion != "" {
				current = fmt.Sprintf("%s +%d builds, %s", u.CurrentVersion, u.BuildsAhead, u.Current)
			}
			return latest, current, u.Newer
		}}
	srv.Alerts = monitor
	go monitor.Run(ctx, 15*time.Second)
	scheduler := &schedule.Runner{Router: router, Store: st, Log: log,
		OnResult: func(ctx context.Context, r schedule.Rule, result string) {
			monitor.ScheduleResult(ctx, r.ID, r.Action, r.Model, result)
		}}
	srv.Schedule = scheduler
	go scheduler.Run(ctx)
	errc := make(chan error, 2)
	var servers []*http.Server

	if cfg.Socket != "" {
		os.MkdirAll(filepath.Dir(cfg.Socket), 0o750)
		os.Remove(cfg.Socket)
		l, err := net.Listen("unix", cfg.Socket)
		if err != nil {
			return fmt.Errorf("listen %s: %w", cfg.Socket, err)
		}
		os.Chmod(cfg.Socket, 0o660)
		hs := &http.Server{Handler: logRequests(log, mux)}
		servers = append(servers, hs)
		go func() { errc <- hs.Serve(l) }()
		log.Info("listening", "socket", cfg.Socket)
	}
	if cfg.Listen != "" {
		l, err := net.Listen("tcp", cfg.Listen)
		if err != nil {
			return fmt.Errorf("listen %s: %w", cfg.Listen, err)
		}
		var h http.Handler = mux
		if cfg.AllowUnauthenticated {
			if !config.IsLoopback(cfg.Listen) {
				log.Warn("UI/API is reachable without authentication", "addr", cfg.Listen)
			}
		} else if cfg.AuthToken != "" || cfg.PasswordHash != "" {
			a := &auth{token: cfg.AuthToken, readToken: cfg.ReadToken, passwordHash: cfg.PasswordHash, secure: cfg.TLSEnabled()}
			if cfg.PasswordHash != "" {
				if a.sessionKey, err = sessionKey(filepath.Join(cfg.DataDir, "session.key")); err != nil {
					return err
				}
			}
			h = a.handler(h)
		}
		hs := &http.Server{Handler: logRequests(log, h), ReadHeaderTimeout: 30 * time.Second}
		servers = append(servers, hs)
		if cfg.TLSEnabled() {
			certFile, keyFile := cfg.TLSCert, cfg.TLSKey
			if cfg.TLS == "self-signed" {
				if certFile, keyFile, err = tlscert.Ensure(filepath.Join(cfg.DataDir, "tls")); err != nil {
					return fmt.Errorf("self-signed certificate: %w", err)
				}
			}
			fp, _ := tlscert.Fingerprint(certFile)
			go func() { errc <- hs.ServeTLS(l, certFile, keyFile) }()
			log.Info("listening", "addr", "https://"+cfg.Listen, "cert", certFile, "sha256", fp)
		} else {
			go func() { errc <- hs.Serve(l) }()
			log.Info("listening", "addr", "http://"+cfg.Listen)
		}
	}
	if len(servers) == 0 {
		return errors.New("neither socket nor listen configured")
	}
	if os.Geteuid() != 0 && os.Getenv("USER") != install.User {
		log.Info("note: not running as the service user; restarts need sudo")
	}
	select {
	case <-ctx.Done():
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, hs := range servers {
		hs.Shutdown(sctx)
	}
	return nil
}

// observingSink stores parsed requests and feeds the Prometheus counters.
type observingSink struct {
	*store.Store
	prom *metrics.Collector
}

func (o observingSink) AddRequests(rs []usagestats.Request) error {
	o.prom.Observe(rs)
	return o.Store.AddRequests(rs)
}

// activeBinaryResolver returns the llama-server binary the router service currently runs
// (it changes when the updater switches versions), cached for a few seconds.
func activeBinaryResolver(cfg config.Server) func(context.Context) string {
	var mu sync.Mutex
	var bin string
	var at time.Time
	return func(ctx context.Context) string {
		mu.Lock()
		defer mu.Unlock()
		if bin != "" && time.Since(at) < 5*time.Second {
			return bin
		}
		if b, err := install.ActiveBinary(ctx, cfg.Router.Service); err == nil && b != "" {
			bin, at = b, time.Now()
			return bin
		}
		return cfg.Router.Binary
	}
}

// rewriteAPI re-adds the /api prefix after StripPrefix("/api/hosts/local").
func rewriteAPI(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.URL.Path = "/api" + r.URL.Path
		r.URL.RawPath = ""
		h.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (s *statusRecorder) WriteHeader(c int) { s.code = c; s.ResponseWriter.WriteHeader(c) }
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func logRequests(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, code: 200}
		t := time.Now()
		next.ServeHTTP(rec, r)
		if r.Method != http.MethodGet || rec.code >= 400 {
			log.Info("http", "method", r.Method, "path", r.URL.Path, "status", rec.code, "ms", time.Since(t).Milliseconds())
		}
	})
}
