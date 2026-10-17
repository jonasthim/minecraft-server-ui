// minecraft-ui serves the native Minecraft manager and its embedded browser UI.
package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/go-chi/chi/v5"
	"github.com/jonasthim/minecraft-server-ui/internal/api"
	"github.com/jonasthim/minecraft-server-ui/internal/auth"
	"github.com/jonasthim/minecraft-server-ui/internal/backup"
	"github.com/jonasthim/minecraft-server-ui/internal/catalog"
	"github.com/jonasthim/minecraft-server-ui/internal/config"
	"github.com/jonasthim/minecraft-server-ui/internal/console"
	"github.com/jonasthim/minecraft-server-ui/internal/content"
	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/events/game"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
	"github.com/jonasthim/minecraft-server-ui/internal/launcher"
	"github.com/jonasthim/minecraft-server-ui/internal/lifecycle"
	"github.com/jonasthim/minecraft-server-ui/internal/managerstate"
	"github.com/jonasthim/minecraft-server-ui/internal/mapdata"
	"github.com/jonasthim/minecraft-server-ui/internal/mapview"
	"github.com/jonasthim/minecraft-server-ui/internal/metrics"
	"github.com/jonasthim/minecraft-server-ui/internal/modpack"
	"github.com/jonasthim/minecraft-server-ui/internal/notify"
	"github.com/jonasthim/minecraft-server-ui/internal/players"
	managedruntime "github.com/jonasthim/minecraft-server-ui/internal/runtime"
	"github.com/jonasthim/minecraft-server-ui/internal/scheduler"
	"github.com/jonasthim/minecraft-server-ui/internal/selfupdate"
	"github.com/jonasthim/minecraft-server-ui/internal/server/fabric"
	"github.com/jonasthim/minecraft-server-ui/internal/server/forge"
	"github.com/jonasthim/minecraft-server-ui/internal/server/neoforge"
	"github.com/jonasthim/minecraft-server-ui/internal/server/paper"
	"github.com/jonasthim/minecraft-server-ui/internal/server/vanilla"
	"github.com/jonasthim/minecraft-server-ui/internal/supervisor"
	"github.com/jonasthim/minecraft-server-ui/internal/update"
	"github.com/jonasthim/minecraft-server-ui/internal/world"
	"github.com/jonasthim/minecraft-server-ui/web"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sync/atomic"
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
	if handled, err := runSelfUpdateProbe(ctx, args, out); handled {
		return err
	}
	command := "serve"
	if len(args) > 0 {
		command = args[0]
	}
	if command == "native-inspect" {
		return runNativeInspect(ctx, args[1:], out)
	}
	if command == "admin-reset" {
		return runAdminReset(ctx, args[1:], os.Stdin, out)
	}
	if command == "manager-state" {
		return runManagerState(ctx, args[1:], out)
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
	if err := managerstate.CheckStartup(ctx, cfg.DataDir); err != nil {
		return err
	}
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
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		var receipt *selfupdate.DrainReceipt
		var receiptErr error
		if runErr == nil {
			receipt, receiptErr = selfupdate.PrepareDrainReceipt(cleanup, database, cfg.DataDir, version)
		}
		runErr = errors.Join(runErr, receiptErr, database.Close())
		if runErr == nil {
			runErr = selfupdate.PublishDrainReceipt(cleanup, cfg.DataDir, receipt)
		}
	}()
	frontend, err := web.Dist()
	if err != nil {
		return fmt.Errorf("embedded frontend: %w", err)
	}
	trust, trustErr := selfupdate.LoadNativeTrust(ctx)
	if trustErr != nil {
		log.Warn("Manager updates disabled: native release trust is unavailable")
		trust = selfupdate.Trust{}
	}
	managerUpdates := selfupdate.New(selfupdate.Config{Trust: trust, CurrentVersion: version, Schema: db.LatestVersion(), StageDir: filepath.Join(cfg.DataDir, "selfupdate", "stages"), DB: database, Native: selfupdate.NewNative(cfg.DataDir, cfg.Address)})
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
	var instanceUpdates *update.Service
	deferUpdate := func(ctx context.Context, id string) (bool, error) {
		if instanceUpdates == nil {
			return false, domain.E(domain.CodeUnavailable, "update startup coordination unavailable")
		}
		return instanceUpdates.StartupBlocked(ctx, id)
	}
	instances := instance.New(database, paths, guard, instance.WithJobMutationGuard(guard), instance.WithActivationCoordinator(activation), instance.WithRecoveryMutationGuard(guard), instance.WithReconcileHold(deferUpdate), instance.WithPendingSettingsGuard(guard))
	control := console.New(database, paths, native, instances, queue)
	playerService := players.New(database, paths, instances, control, queue, native)
	gameEvents := game.New(database, control, instances)
	var schedules *scheduler.Service
	operations := lifecycle.New(lifecycle.Config{DeferAutostart: deferUpdate, StopIntent: func(ctx context.Context, id string, actor domain.User) error {
		if schedules == nil {
			return domain.E(domain.CodeUnavailable, "crash recovery coordination unavailable")
		}
		return schedules.CancelRecovery(ctx, id, actor)
	}, DB: database, DataDir: cfg.DataDir, Instances: instances, Jobs: queue, Native: native, Runtime: managedruntime.New(cfg.DataDir, nil), Console: control, Providers: []domain.DistributionProvider{vanilla.New(cfg.DataDir, nil), paper.New(cfg.DataDir, nil), fabric.New(cfg.DataDir, nil), forge.New(cfg.DataDir, nil), neoforge.New(cfg.DataDir, nil)}})
	activation.operations = operations
	worlds := world.New(database, instances, paths, queue)
	maps := mapview.New(mapdata.New(worlds, paths), control)
	metricService := metrics.New(metrics.Config{DB: database, Paths: paths, Instances: instances, Native: native, Players: playerService})
	notificationService := notify.New(notify.Config{DB: database})
	backups := backup.New(backup.Config{DB: database, Paths: paths, Instances: instances, Jobs: queue, Console: control, Native: native, Worlds: worlds})
	if err := backups.Register(); err != nil {
		return err
	}
	catalogs, err := catalog.New(catalog.Config{DB: database, ArtifactDir: filepath.Join(cfg.DataDir, "catalog", "artifacts"), UserAgent: "minecraft-server-ui/" + version + " (+https://github.com/jonasthim/minecraft-server-ui)"})
	if err != nil {
		return err
	}
	contents, err := content.New(content.Config{LanguageEvidence: operations.LanguageEvidence, DB: database, Paths: paths, Instances: instances, Queue: queue, Catalog: catalogs, Recovery: guard})
	if err != nil {
		return err
	}
	packs, err := modpack.New(modpack.Config{DB: database, Paths: paths, Instances: instances, Queue: queue, Catalog: catalogs, Content: contents, Recovery: guard})
	if err != nil {
		return err
	}
	instanceUpdates, err = update.New(update.Config{DB: database, Paths: paths, Instances: instances, Jobs: queue, Lifecycle: operations, Backups: backups, Content: contents, Packs: packs, BeginUpdateStop: func(ctx context.Context, execution *jobs.Execution, actor domain.User) (int64, lifecycle.RecoveryStartGuard, error) {
		if schedules == nil {
			return 0, nil, domain.E(domain.CodeUnavailable, "update stop coordination unavailable")
		}
		return schedules.BeginUpdateStop(ctx, execution, actor)
	}})
	if err != nil {
		return err
	}
	rollback, err := update.NewRollbackCoordinator(instanceUpdates)
	if err != nil {
		return err
	}
	if err := instanceUpdates.SetRollbackCoordinator(rollback); err != nil {
		return err
	}
	schedules = scheduler.New(scheduler.Config{DB: database, Commands: control, Backups: backups, Restarts: operations, Updates: instanceUpdates, Native: native, Lifecycle: operations, Jobs: queue, Log: log, OnCrash: func(ctx context.Context, event scheduler.CrashEvent) error {
		return notificationService.EnqueueServerCrash(ctx, event.ID, event.InstanceID, event.OccurredAt)
	}})
	if err := packs.Register(); err != nil {
		return err
	}
	if err := schedules.RegisterJobs(); err != nil {
		return err
	}
	if err := instanceUpdates.Register(); err != nil {
		return err
	}
	guard.updateReady = instanceUpdates.CheckReady
	guard.updateJobReady = instanceUpdates.CheckJobReady
	guard.ready = func(ctx context.Context, id string) error {
		if err := backups.CheckReady(ctx, id); err != nil {
			return err
		}
		if err := contents.CheckReady(ctx, id); err != nil {
			return err
		}
		return packs.CheckReady(ctx, id)
	}
	if err := contents.Register(); err != nil {
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
	if err := reconcileStartup(ctx, queue, native, instances, operations, worlds, backups, contents, packs, instanceUpdates, schedules, guard, log, runtime.GOOS == "windows"); err != nil {
		return err
	}
	var ready atomic.Bool
	updateReadiness := selfupdate.NewReadiness(cfg.DataDir, version, db.LatestVersion(), func() bool {
		if !ready.Load() || guard.blocked != nil {
			return false
		}
		probe, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		return database.PingContext(probe) == nil
	})
	handler := api.NewRouter(api.Dependencies{DB: database, Logger: log, Web: frontend, Mount: func(r chi.Router) {
		r.Method("GET", "/internal/update-ready", updateReadiness)
		mountAPIWithFeatures(r, identity, queue, instances, func(r chi.Router) {
			api.MountLifecycle(r, operations)
			api.MountConsole(r, control)
			api.MountPlayers(r, playerService)
			api.MountGameEvents(r, gameEvents)
			api.MountWorlds(r, worlds)
			api.MountMap(r, maps)
			api.MountBackups(r, backups)
			api.MountCatalog(r, catalogs)
			api.MountContent(r, contents)
			api.MountModpacks(r, packs)
			api.MountUpdates(r, instanceUpdates)
			api.MountSchedules(r, schedules)
			api.MountMetrics(r, metricService)
			api.MountNotifications(r, notificationService)
			api.MountSelfUpdate(r, managerUpdates)
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
	observationCtx, cancelObservations := context.WithCancel(ctx)
	observationLoops := []struct {
		name string
		run  func(context.Context) error
	}{{"metrics", metricService.Run}, {"notifications", notificationService.Run}, {"game events", gameEvents.Run}, {"schedules", schedules.Run}}
	observationDone := make(chan error, len(observationLoops))
	for _, loop := range observationLoops {
		go func(name string, run func(context.Context) error) {
			observationDone <- watchObservability(observationCtx, name, run, func(shutdown context.Context) error {
				if err := server.Shutdown(shutdown); err != nil {
					return errors.Join(err, server.Close())
				}
				return nil
			}, log)
		}(loop.name, loop.run)
	}
	defer func() {
		cancelObservations()
		for range observationLoops {
			runErr = errors.Join(runErr, <-observationDone)
		}
	}()

	done := make(chan struct{})
	shutdownDone := make(chan error, 1)
	go func() {
		select {
		case <-ctx.Done():
			ready.Store(false)
			shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := server.Shutdown(shutdown); err != nil {
				shutdownDone <- fmt.Errorf("drain HTTP: %w", errors.Join(err, server.Close()))
				return
			}
		case <-done:
		}
		shutdownDone <- nil
	}()
	log.Info("Minecraft manager listening", "address", cfg.Address, "version", version)
	ready.Store(guard.blocked == nil)
	serveErr := server.ListenAndServe()
	ready.Store(false)
	close(done)
	// A failed drain must reach the deferred cleanup and prevent an update receipt.
	return httpServeResult(serveErr, <-shutdownDone)
}

func httpServeResult(serveErr, shutdownErr error) error {
	if errors.Is(serveErr, http.ErrServerClosed) {
		serveErr = nil
	}
	if serveErr != nil {
		serveErr = fmt.Errorf("serve HTTP: %w", serveErr)
	}
	return errors.Join(serveErr, shutdownErr)
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
	ready   func(context.Context, string) error
	// Ordinary backup/content/pack readiness always runs first. Only the update
	// gate may recognize the original separately confirmed rollback execution.
	updateReady    func(context.Context, string) error
	updateJobReady func(context.Context, *jobs.Execution, string) error
}

func (g *stoppedMutationGuard) WithStopped(ctx context.Context, id string, fn func() error) error {
	if g.blocked != nil {
		return domain.E(domain.CodeUnavailable, "instance reconciliation requires administrator repair")
	}
	return g.queue.WithIdle(ctx, id, func() error {
		return g.withNativeStopped(ctx, id, func() error {
			if g.ready != nil {
				if err := g.ready(ctx, id); err != nil {
					return err
				}
			}
			if g.updateReady != nil {
				if err := g.updateReady(ctx, id); err != nil {
					return err
				}
			}
			return fn()
		})
	})
}

// Pending drafts change only SQL metadata. Serialize with the same queue and
// reconciliation gates, while allowing an independently running game process.
func (g *stoppedMutationGuard) WithPendingSettings(ctx context.Context, id string, fn func() error) error {
	if g.blocked != nil {
		return domain.E(domain.CodeUnavailable, "instance reconciliation requires administrator repair")
	}
	return g.queue.WithIdle(ctx, id, func() error {
		if g.ready != nil {
			if err := g.ready(ctx, id); err != nil {
				return err
			}
		}
		if g.updateReady != nil {
			if err := g.updateReady(ctx, id); err != nil {
				return err
			}
		}
		return fn()
	})
}

// The active job already owns queue serialization; entering WithIdle here would
// reject its own slot. Keep native stopped admission held exactly once.
func (g *stoppedMutationGuard) WithStoppedForJob(ctx context.Context, execution *jobs.Execution, fn func(string) error) error {
	if g.blocked != nil {
		return domain.E(domain.CodeUnavailable, "instance reconciliation requires administrator repair")
	}
	return execution.WithInstanceMutation(ctx, func(id string) error {
		return g.withNativeStopped(ctx, id, func() error {
			if g.ready != nil {
				if err := g.ready(ctx, id); err != nil {
					return err
				}
			}
			if g.updateJobReady != nil {
				if err := g.updateJobReady(ctx, execution, id); err != nil {
					return err
				}
			} else if g.updateReady != nil {
				if err := g.updateReady(ctx, id); err != nil {
					return err
				}
			}
			return fn(id)
		})
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

// watchObservability fails the manager visibly if a durable observation loop
// exits unexpectedly. Cancellation returns only after the service has drained.
func watchObservability(ctx context.Context, name string, run func(context.Context) error, shutdown func(context.Context) error, log *slog.Logger) error {
	err := run(ctx)
	if ctx.Err() != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil
		}
		return err
	}
	log.Error("Observation service stopped unexpectedly; shutting down manager", "service", name, "code", domain.CodeUnavailable)
	cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return errors.Join(domain.E(domain.CodeUnavailable, name+" observation stopped unexpectedly"), shutdown(cleanup))
}

type startupNative interface {
	Autostart(context.Context, map[string]domain.LaunchSpec) error
}

type startupBackups interface {
	ReconcileLeases(context.Context) error
	ReconcileRestores(context.Context) error
}

type startupContent interface{ Reconcile(context.Context) error }

func reconcileStartup(ctx context.Context, queue *jobs.Service, native startupNative, instances *instance.Service, operations *lifecycle.Service, worlds *world.Service, backups startupBackups, contents, packs, updates, schedules startupContent, guard *stoppedMutationGuard, log *slog.Logger, windows bool) error {
	// Recover durable work without dispatch until files and previous processes
	// have been reconciled. Linux games keep running across manager restarts.
	if err := queue.StartPaused(ctx); err != nil {
		return err
	}
	if err := native.Autostart(ctx, nil); err != nil {
		return fmt.Errorf("native prior-process reconciliation: %w", err)
	}
	if backups == nil {
		return domain.E(domain.CodeUnavailable, "backup startup recovery is unavailable")
	}
	if err := backups.ReconcileLeases(ctx); err != nil {
		return fmt.Errorf("backup saving recovery: %w", err)
	}
	if err := backups.ReconcileRestores(ctx); err != nil {
		return fmt.Errorf("backup restore recovery: %w", err)
	}
	if contents == nil {
		return domain.E(domain.CodeUnavailable, "content startup recovery is unavailable")
	}
	if err := contents.Reconcile(ctx); err != nil {
		return fmt.Errorf("content recovery: %w", err)
	}
	if packs == nil {
		return domain.E(domain.CodeUnavailable, "modpack startup recovery is unavailable")
	}
	if err := packs.Reconcile(ctx); err != nil {
		return fmt.Errorf("modpack recovery: %w", err)
	}
	// Instance reconciliation materializes selected server files. Complete update
	// repair first so it cannot overwrite evidence needed by the update journal.
	if updates == nil {
		return domain.E(domain.CodeUnavailable, "update startup recovery is unavailable")
	}
	if err := updates.Reconcile(ctx); err != nil {
		return fmt.Errorf("update recovery: %w", err)
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
		if schedules == nil {
			return domain.E(domain.CodeUnavailable, "schedule startup recovery is unavailable")
		}
		if err := schedules.Reconcile(ctx); err != nil {
			return fmt.Errorf("schedule recovery: %w", err)
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
