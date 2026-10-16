// minecraft-ui serves the native Minecraft manager and its embedded browser UI.
package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/go-chi/chi/v5"
	"github.com/jonasthim/minecraft-server-ui/internal/api"
	"github.com/jonasthim/minecraft-server-ui/internal/auth"
	"github.com/jonasthim/minecraft-server-ui/internal/catalog"
	"github.com/jonasthim/minecraft-server-ui/internal/config"
	"github.com/jonasthim/minecraft-server-ui/internal/console"
	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
	"github.com/jonasthim/minecraft-server-ui/internal/launcher"
	"github.com/jonasthim/minecraft-server-ui/internal/lifecycle"
	"github.com/jonasthim/minecraft-server-ui/internal/players"
	managedruntime "github.com/jonasthim/minecraft-server-ui/internal/runtime"
	"github.com/jonasthim/minecraft-server-ui/internal/server/fabric"
	"github.com/jonasthim/minecraft-server-ui/internal/server/forge"
	"github.com/jonasthim/minecraft-server-ui/internal/server/neoforge"
	"github.com/jonasthim/minecraft-server-ui/internal/server/paper"
	"github.com/jonasthim/minecraft-server-ui/internal/server/vanilla"
	"github.com/jonasthim/minecraft-server-ui/internal/supervisor"
	"github.com/jonasthim/minecraft-server-ui/internal/world"
	"github.com/jonasthim/minecraft-server-ui/web"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
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
	activation := &activationCoordinator{}
	instances := instance.New(database, paths, guard, instance.WithJobMutationGuard(guard), instance.WithActivationCoordinator(activation), instance.WithRecoveryMutationGuard(guard))
	control := console.New(database, paths, native, instances, queue)
	playerService := players.New(database, paths, instances, control, queue, native)
	operations := lifecycle.New(lifecycle.Config{DB: database, DataDir: cfg.DataDir, Instances: instances, Jobs: queue, Native: native, Runtime: managedruntime.New(cfg.DataDir, nil), Console: control, Providers: []domain.DistributionProvider{vanilla.New(cfg.DataDir, nil), paper.New(cfg.DataDir, nil), fabric.New(cfg.DataDir, nil), forge.New(cfg.DataDir, nil), neoforge.New(cfg.DataDir, nil)}})
	activation.operations = operations
	worlds := world.New(database, instances, paths, queue)
	catalogs, err := catalog.New(catalog.Config{DB: database, ArtifactDir: filepath.Join(cfg.DataDir, "catalog", "artifacts"), UserAgent: "minecraft-server-ui/" + version + " (+https://github.com/jonasthim/minecraft-server-ui)"})
	if err != nil {
		return err
	}
	if err := playerService.Register(); err != nil {
		return err
	}
	if err := control.Register(); err != nil {
		return err
	}
	if err := operations.RegisterJobs(); err != nil {
		return err
	}
	if err := reconcileStartup(ctx, queue, native, instances, operations, worlds, guard, log, runtime.GOOS == "windows"); err != nil {
		return err
	}
	handler := api.NewRouter(api.Dependencies{DB: database, Logger: log, Web: frontend, Mount: func(r chi.Router) {
		mountAPIWithFeatures(r, identity, queue, instances, func(r chi.Router) {
			api.MountLifecycle(r, operations)
			api.MountConsole(r, control)
			api.MountPlayers(r, playerService)
			api.MountWorlds(r, worlds)
			api.MountCatalog(r, catalogs)
		})
	}})
	server := &http.Server{Addr: cfg.Address, Handler: handler, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 1 << 20}
	consoleCtx, cancelConsole := context.WithCancel(ctx)
	consoleDone := make(chan error, 1)
	go func() {
		consoleDone <- watchConsole(consoleCtx, control.Run, func(shutdown context.Context) error {
			if err := server.Shutdown(shutdown); err != nil {
				return errors.Join(err, server.Close())
			}
			return nil
		}, log)
	}()
	defer func() { cancelConsole(); runErr = errors.Join(runErr, <-consoleDone) }()
	playerCtx, cancelPlayers := context.WithCancel(ctx)
	playerDone := make(chan error, 1)
	go func() {
		playerDone <- watchPlayers(playerCtx, playerService.Run, func(shutdown context.Context) error {
			if err := server.Shutdown(shutdown); err != nil {
				return errors.Join(err, server.Close())
			}
			return nil
		}, log)
	}()
	defer func() { cancelPlayers(); runErr = errors.Join(runErr, <-playerDone) }()
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
	var current *instance.Service
	if len(instances) > 0 {
		current = instances[0]
	}
	mountAPIWithFeatures(r, identity, queue, current)
}

func mountAPIWithFeatures(r chi.Router, identity *auth.Service, queue *jobs.Service, instances *instance.Service, features ...func(chi.Router)) {
	r.Route("/api/v1", func(r chi.Router) {
		r.Use(identity.Authenticate, identity.CSRF)
		identity.MountRoutes(r)
		api.MountJobs(r, queue)
		if instances != nil {
			api.MountInstances(r, instances)
		}
		for _, mount := range features {
			mount(r)
		}
	})
}

// Global queue admission wraps native lifecycle exclusion. The callback must not
// enqueue jobs or re-enter either guard. Startup sets blocked before HTTP starts.
type stoppedMutationGuard struct {
	queue  *jobs.Service
	native interface {
		WithStopped(context.Context, string, func() error) error
	}
	blocked error
}

func (g *stoppedMutationGuard) WithStopped(ctx context.Context, id string, fn func() error) error {
	if g.blocked != nil {
		return domain.E(domain.CodeUnavailable, "instance reconciliation requires administrator repair")
	}
	return g.queue.WithIdle(ctx, id, func() error { return g.withNativeStopped(ctx, id, fn) })
}

// The active job already owns queue serialization; entering WithIdle here would
// reject its own slot. Keep native stopped admission held exactly once.
func (g *stoppedMutationGuard) WithStoppedForJob(ctx context.Context, execution *jobs.Execution, fn func(string) error) error {
	if g.blocked != nil {
		return domain.E(domain.CodeUnavailable, "instance reconciliation requires administrator repair")
	}
	return execution.WithInstanceMutation(ctx, func(id string) error {
		return g.withNativeStopped(ctx, id, func() error { return fn(id) })
	})
}

// A dead observation loop must not leave a seemingly healthy HTTP service.
// Drain the listener on unexpected exit and return a safe failure to the host.
func watchConsole(ctx context.Context, run func(context.Context) error, shutdown func(context.Context) error, log *slog.Logger) error {
	err := run(ctx)
	if ctx.Err() != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil
		}
		return err
	}
	log.Error("Console observation stopped unexpectedly; shutting down manager", "code", domain.CodeUnavailable)
	cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return errors.Join(domain.E(domain.CodeUnavailable, "console observation stopped unexpectedly"), shutdown(cleanup))
}

func watchPlayers(ctx context.Context, run func(context.Context) error, shutdown func(context.Context) error, log *slog.Logger) error {
	err := run(ctx)
	if ctx.Err() != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil
		}
		return err
	}
	log.Error("Player observation stopped unexpectedly; shutting down manager", "code", domain.CodeUnavailable)
	cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return errors.Join(domain.E(domain.CodeUnavailable, "player observation stopped unexpectedly"), shutdown(cleanup))
}

type startupNative interface {
	Autostart(context.Context, map[string]domain.LaunchSpec) error
}

func reconcileStartup(ctx context.Context, queue *jobs.Service, native startupNative, instances *instance.Service, operations *lifecycle.Service, worlds *world.Service, guard *stoppedMutationGuard, log *slog.Logger, windows bool) error {
	// Recover durable work without dispatch until files and previous processes
	// have been reconciled. Linux games keep running across manager restarts.
	if err := queue.StartPaused(ctx); err != nil {
		return err
	}
	if err := native.Autostart(ctx, nil); err != nil {
		return fmt.Errorf("native prior-process reconciliation: %w", err)
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
	if reconcileErr == nil {
		if err := operations.Reconcile(ctx); err != nil {
			return fmt.Errorf("lifecycle recovery: %w", err)
		}
		if err := worlds.ReconcileExports(ctx); err != nil {
			return fmt.Errorf("world export recovery: %w", err)
		}
		if windows {
			plans, err := operations.AutostartPlans(ctx)
			if err != nil {
				return fmt.Errorf("validate autostart: %w", err)
			}
			if err := native.Autostart(ctx, plans); err != nil {
				return fmt.Errorf("native autostart: %w", err)
			}
		}
		if err := operations.VerifyRunningAfterReconcile(ctx); err != nil {
			return fmt.Errorf("queue startup readiness: %w", err)
		}
		if err := queue.Resume(); err != nil {
			return err
		}
	}

	return nil
}

type activationCoordinator struct{ operations *lifecycle.Service }

func (c *activationCoordinator) PrepareConfigActivation(ctx context.Context, id string, memoryMiB int) (instance.ConfigActivation, error) {
	if c.operations == nil {
		return nil, domain.E(domain.CodeUnavailable, "configuration activation coordination is unavailable")
	}
	return c.operations.PrepareConfigActivation(ctx, id, memoryMiB)
}

func (g *stoppedMutationGuard) WithStoppedForRecovery(ctx context.Context, id string, fn func() error) error {
	if g.blocked != nil {
		return domain.E(domain.CodeUnavailable, "instance reconciliation requires administrator repair")
	}
	return g.queue.WithPausedInstance(ctx, id, func() error { return g.withNativeStopped(ctx, id, fn) })
}

// Distinguish a failed native admission from an error inside the callback so
// provider/config errors never get mislabeled as host service failures.
func (g *stoppedMutationGuard) withNativeStopped(ctx context.Context, id string, fn func() error) error {
	entered := false
	err := g.native.WithStopped(ctx, id, func() error { entered = true; return fn() })
	if err != nil && !entered && ctx.Err() == nil && domain.AsError(err).Code != domain.CodeInstanceRunning {
		return domain.E(domain.CodeUnavailable, "Native service is unavailable or does not match this manager configuration. Install or repair the approved native service before retrying.")
	}
	return err
}
