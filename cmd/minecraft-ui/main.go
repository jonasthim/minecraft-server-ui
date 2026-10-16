// minecraft-ui serves the native Minecraft manager and its embedded browser UI.
package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/go-chi/chi/v5"
	"github.com/jonasthim/minecraft-server-ui/internal/api"
	"github.com/jonasthim/minecraft-server-ui/internal/auth"
	"github.com/jonasthim/minecraft-server-ui/internal/config"
	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
	"github.com/jonasthim/minecraft-server-ui/internal/launcher"
	"github.com/jonasthim/minecraft-server-ui/internal/supervisor"
	"github.com/jonasthim/minecraft-server-ui/web"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

var version = "0.0.0-dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		slog.Error("minecraft-ui stopped", "error", err)
		os.Exit(1)
	}
}
func run(ctx context.Context, args []string, out io.Writer) error {
	command := "serve"
	if len(args) > 0 {
		command = args[0]
	}
	if command == "native-inspect" {
		return runNativeInspect(ctx, args[1:], out)
	}
	if command == "launch" {
		return runLaunch(ctx, args[1:], os.Stdin)
	}
	if len(args) > 1 {
		return fmt.Errorf("usage: minecraft-ui [serve|version]")
	}
	switch command {
	case "version":
		_, err := fmt.Fprintf(out, "minecraft-ui %s\n", version)
		return err
	case "serve":
		cfg, err := config.FromEnvironment()
		if err != nil {
			return err
		}
		if handled, err := runNativeService(ctx, func(serviceCtx context.Context) error { return serve(serviceCtx, cfg) }); handled || err != nil {
			return err
		}
		return serve(ctx, cfg)
	default:
		return fmt.Errorf("unknown command %q (expected serve or version)", command)
	}
}
func serve(ctx context.Context, cfg config.Config) (runErr error) {
	release, err := launcher.AcquireManager(cfg.DataDir)
	if err != nil {
		return err
	}
	defer release()
	log := slog.Default()
	native, err := supervisor.New(supervisor.Config{DataDir: cfg.DataDir})
	if err != nil {
		return err
	}
	paths, err := instance.NewPaths(cfg.DataDir)
	if err != nil {
		return err
	}
	database, err := db.Open(ctx, filepath.Join(cfg.DataDir, "manager.db"))
	if err != nil {
		return err
	}
	defer database.Close()
	frontend, err := web.Dist()
	if err != nil {
		return fmt.Errorf("embedded frontend: %w", err)
	}
	identity := auth.NewService(database, auth.Config{BaseURL: cfg.BaseURL, InsecureCookies: cfg.InsecureCookies}, log)
	queue := jobs.New(database, log)
	// Services drain before DB Close; the manager lock is released last. Closing
	// every Windows parent pipe first makes JVM shutdown concurrent across instances.
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 110*time.Second)
		defer cancel()
		results := make(chan error, 2)
		go func() { results <- queue.Stop(cleanup) }()
		go func() { results <- native.Shutdown(cleanup) }()
		runErr = errors.Join(runErr, <-results, <-results)
	}()
	guard := &stoppedMutationGuard{queue: queue, native: native}
	instances := instance.New(database, paths, guard)
	// Feature packages register their approved durable handlers before Start.
	if err := queue.Start(ctx); err != nil {
		return err
	}
	reconciled, reconcileErr := instances.Reconcile(ctx)
	for _, pending := range reconciled.Deferred {
		log.Warn("instance reconciliation deferred", "instance_id", pending.InstanceID, "code", pending.Code)
	}
	if reconcileErr != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		guard.blocked = reconcileErr
		log.Error("instance reconciliation requires administrator repair; mutations blocked", "error", reconcileErr)
	}
	// Providers do not yet supply validated installed LaunchSpecs. An empty map
	// reconciles previous Windows JVMs and waits, without pretending to autostart.
	if err := native.Autostart(ctx, nil); err != nil {
		return fmt.Errorf("native startup reconciliation: %w", err)
	}
	handler := api.NewRouter(api.Dependencies{DB: database, Logger: log, Web: frontend, Mount: func(r chi.Router) { mountAPI(r, identity, queue, instances) }})
	server := &http.Server{Addr: cfg.Address, Handler: handler, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 1 << 20}
	done := make(chan struct{})
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := server.Shutdown(shutdown); err != nil {
				log.Error("HTTP shutdown", "error", err)
				_ = server.Close()
			}
		case <-done:
		}
	}()
	log.Info("Minecraft manager listening", "address", cfg.Address, "version", version)
	serveErr := server.ListenAndServe()
	close(done)
	<-shutdownDone // Finish draining HTTP before closing the shared database.
	if errors.Is(serveErr, http.ErrServerClosed) {
		serveErr = nil
	}
	if serveErr != nil {
		serveErr = fmt.Errorf("serve HTTP: %w", serveErr)
	}
	return serveErr
}

func mountAPI(r chi.Router, identity *auth.Service, queue *jobs.Service, instances ...*instance.Service) {
	r.Route("/api/v1", func(r chi.Router) {
		r.Use(identity.Authenticate, identity.CSRF)
		identity.MountRoutes(r)
		api.MountJobs(r, queue)
		if len(instances) > 0 && instances[0] != nil {
			api.MountInstances(r, instances[0])
		}
	})
}

// Global queue admission wraps native lifecycle exclusion. The callback must not
// enqueue jobs or re-enter either guard. Startup sets blocked before HTTP starts.
type stoppedMutationGuard struct {
	queue   *jobs.Service
	native  *supervisor.Supervisor
	blocked error
}

func (g *stoppedMutationGuard) WithStopped(ctx context.Context, id string, fn func() error) error {
	if g.blocked != nil {
		return domain.E(domain.CodeUnavailable, "instance reconciliation requires administrator repair")
	}
	return g.queue.WithIdle(ctx, id, func() error { return g.native.WithStopped(ctx, id, fn) })
}
