package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/cors"
	"github.com/spf13/cobra"

	"github.com/soroworks/sorovault/internal/api"
	"github.com/soroworks/sorovault/internal/config"
	"github.com/soroworks/sorovault/internal/refresher"
	"github.com/soroworks/sorovault/internal/registry"
	"github.com/soroworks/sorovault/internal/store"
	"github.com/soroworks/sorovault/internal/web"
)

// shutdownGrace is how long in-flight requests get to finish after a signal.
const shutdownGrace = 15 * time.Second

// requestTimeout bounds any single request. Registering a contract does two
// RPC round trips, so this is generous compared with a read-only API.
const requestTimeout = 60 * time.Second

func newServeCmd() *cobra.Command {
	var addr string

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve the JSON API and browse UI",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return withApp(cmd.Context(), func(ctx context.Context, a *app) error {
				listenAddr := a.cfg.HTTPAddr
				if addr != "" {
					listenAddr = addr
				}

				refresh, stopRefresh, err := startRefresher(ctx, a)
				if err != nil {
					return err
				}
				// Stopped before withApp closes the store, so a sweep is
				// never left writing to a closed pool.
				defer stopRefresh()

				handler, err := newRouter(a.registry, a.log, refresh, a.cfg)
				if err != nil {
					return err
				}

				return serve(ctx, listenAddr, handler, a.log)
			})
		},
	}

	cmd.Flags().StringVar(&addr, "addr", "", "listen address (overrides HTTP_ADDR)")
	return cmd
}

// startRefresher runs automatic refresh in the background when
// REFRESH_INTERVAL is set. It returns nil when refresh is off, and a stop
// function that cancels it and waits for an in-progress sweep to finish.
func startRefresher(ctx context.Context, a *app) (r *refresher.Refresher, stop func(), err error) {
	if a.cfg.RefreshInterval <= 0 {
		return nil, func() {}, nil
	}

	notifier, err := refreshNotifier(a)
	if err != nil {
		return nil, nil, err
	}

	r, err = refresher.New(a.registry, refresher.Options{
		Interval: a.cfg.RefreshInterval,
		Timeout:  a.cfg.RPCTimeout,
		Notifier: notifier,
		Logger:   a.log.With("component", "refresher"),
	})
	if err != nil {
		return nil, nil, err
	}

	stop = startRefresherLoop(ctx, a.cfg.RefreshInterval, r.Run)
	a.log.Info("automatic refresh enabled", "interval", a.cfg.RefreshInterval)
	return r, stop, nil
}

func startRefresherLoop(ctx context.Context, interval time.Duration, run func(context.Context) error) func() {
	if interval <= 0 {
		return func() {}
	}

	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = run(ctx)
	}()

	return func() {
		cancel()
		<-done
	}
}

// refreshNotifier builds the webhook a sweep posts upgrades to, if one is
// configured.
func refreshNotifier(a *app) (refresher.Notifier, error) {
	if a.cfg.RefreshWebhookURL == "" {
		return nil, nil
	}

	webhook, err := refresher.NewWebhook(refresher.WebhookOptions{
		URL:    a.cfg.RefreshWebhookURL,
		Secret: a.cfg.RefreshWebhookSecret,
		Logger: a.log.With("component", "refresher-webhook"),
	})
	if err != nil {
		return nil, err
	}

	a.log.Info("refresh webhook enabled", "url", a.cfg.RefreshWebhookURL)
	return webhook, nil
}

// newRouter assembles the JSON API, the browse UI and the health endpoints
// into one handler. refresh may be nil when automatic refresh is off; the
// statistics endpoint then has no sweep to report.
//
// cfg may be omitted or nil in tests, which leaves CORS and rate limiting
// off and uses the readiness probe's own defaults.
func newRouter(reg *registry.Registry, log *slog.Logger, refresh *refresher.Refresher, configs ...*config.Config) (http.Handler, error) {
	var cfg *config.Config
	if len(configs) > 0 {
		cfg = configs[0]
	}

	ui, err := web.NewServer(reg, log)
	if err != nil {
		return nil, err
	}

	if cfg != nil {
		ui.SetPublicURL(cfg.PublicURL)
	}

	server := api.NewServer(reg, log)
	if refresh != nil {
		server.SetLastSweep(refresh.LastSweep)
	}
	if cfg != nil {
		server.SetRateLimit(api.RateLimitOptions{
			PerMinute:          cfg.RateLimitPerMinute,
			Burst:              cfg.RateLimitBurst,
			TrustXForwardedFor: cfg.TrustXForwardedFor,
		})
	}

	r := chi.NewRouter()
	if cfg != nil {
		if middleware := corsMiddleware(cfg.CORSOrigins); middleware != nil {
			r.Use(middleware)
		}
	}
	r.Use(api.Middleware(log)...)

	readiness := newReadinessProbe(reg.Store(), reg.CheckNetwork)
	r.Get("/healthz", healthz)
	r.Get("/readyz", readiness.handle)
	r.Mount("/api", server.Routes())
	r.Mount("/", ui.Routes())

	return http.TimeoutHandler(r, requestTimeout, `{"error":"request timed out"}`), nil
}

// corsMiddleware builds the opt-in CORS layer. It returns nil when no
// origins are configured, so an unconfigured server sends no CORS headers.
//
// A wildcard is restricted to read-only methods. GET responses are public
// registry data, so any origin may read them; the POST routes spend RPC
// calls and database writes on the caller's behalf, so a wildcard must never
// open them. An explicit allowlist may use the write methods because the
// operator has named the clients they trust.
func corsMiddleware(origins []string) func(http.Handler) http.Handler {
	if len(origins) == 0 {
		return nil
	}

	methods := []string{
		http.MethodGet, http.MethodHead, http.MethodOptions,
		http.MethodPost, http.MethodPut,
	}
	if len(origins) == 1 && origins[0] == "*" {
		methods = []string{http.MethodGet, http.MethodHead, http.MethodOptions}
	}

	return cors.Handler(cors.Options{
		AllowedOrigins: origins,
		AllowedMethods: methods,
		ExposedHeaders: []string{"Retry-After"},
		MaxAge:         int((5 * time.Minute).Seconds()),
	})
}

// readinessCacheTTL is how long a readiness answer is reused. A probe runs
// often, so this keeps it from adding load to Postgres and RPC while still
// noticing a dependency that has been down for more than a few seconds.
const readinessCacheTTL = 5 * time.Second

// readinessCheckTimeout bounds one dependency check, independently of the
// other, so a hanging database cannot hide the state of RPC.
const readinessCheckTimeout = 5 * time.Second

// readinessProbe checks the dependencies an instance cannot serve without.
type readinessProbe struct {
	store        store.Store
	checkNetwork func(context.Context) error
	ttl          time.Duration
	now          func() time.Time

	mu        sync.Mutex
	cached    readinessResponse
	checkedAt time.Time
	hasCache  bool
}

type readinessResponse struct {
	Status  string            `json:"status"`
	Version string            `json:"version"`
	Checks  map[string]string `json:"checks"`
}

func newReadinessProbe(s store.Store, checkNetwork func(context.Context) error) *readinessProbe {
	return &readinessProbe{
		store:        s,
		checkNetwork: checkNetwork,
		ttl:          readinessCacheTTL,
		now:          time.Now,
	}
}

// handle serves GET /readyz. Unlike /healthz, it answers "can this instance
// serve?" and is meant for a load balancer or scheduler to take a broken
// instance out of rotation.
func (p *readinessProbe) handle(w http.ResponseWriter, r *http.Request) {
	report, ready := p.report(r.Context())

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	status := http.StatusOK
	if !ready {
		status = http.StatusServiceUnavailable
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(report)
}

func (p *readinessProbe) report(ctx context.Context) (readinessResponse, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := p.now()
	if p.hasCache && p.ttl > 0 && now.Sub(p.checkedAt) < p.ttl {
		return p.cached, p.cached.Status == "ok"
	}

	checks := map[string]string{
		"database": p.check(ctx, p.store.Ping),
		"rpc":      p.check(ctx, p.checkNetwork),
	}
	status := "ok"
	if checks["database"] != "ok" || checks["rpc"] != "ok" {
		status = "unavailable"
	}

	report := readinessResponse{Status: status, Version: version, Checks: checks}
	p.cached = report
	p.checkedAt = now
	p.hasCache = true
	return report, status == "ok"
}

func (p *readinessProbe) check(ctx context.Context, fn func(context.Context) error) string {
	checkCtx, cancel := context.WithTimeout(ctx, readinessCheckTimeout)
	defer cancel()

	if err := fn(checkCtx); err != nil {
		if checkCtx.Err() != nil && checkCtx.Err() == context.DeadlineExceeded {
			return "timed out"
		}
		return err.Error()
	}
	return "ok"
}

// healthz reports process liveness. It deliberately does not touch the
// database or RPC: it answers "is this process serving?", which is what a
// scheduler restarts on, not "are its dependencies healthy?".
func healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status":  "ok",
		"version": version,
	})
}

// serve runs the HTTP server until the context is cancelled or a termination
// signal arrives, then drains in-flight requests.
func serve(ctx context.Context, addr string, handler http.Handler, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
		// WriteTimeout is left unset: the TimeoutHandler above already
		// bounds each request, and a write deadline here would cut
		// responses off without the clean error message it produces.
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", addr, "version", version)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("http server: %w", err)
		}
		return nil

	case <-ctx.Done():
		log.Info("shutting down", "grace", shutdownGrace)

		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownGrace)
		defer cancel()

		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("http shutdown: %w", err)
		}
		return nil
	}
}
