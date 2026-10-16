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
		return serve(ctx, cfg)
	default:
		return fmt.Errorf("unknown command %q (expected serve or version)", command)
	}
}
func serve(ctx context.Context, cfg config.Config) error {
	log := slog.Default()
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
	handler := api.NewRouter(api.Dependencies{DB: database, Logger: log, Web: frontend, Mount: func(r chi.Router) { api.MountAuth(r, identity) }})
	server := &http.Server{Addr: cfg.Address, Handler: handler, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 1 << 20}
	done := make(chan struct{})
	defer close(done)
	go func() {
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
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve HTTP: %w", err)
	}
	return nil
}
