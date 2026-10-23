// Command api boots the wallet HTTP server.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spiderocious/devfest-akure-2026/wallet/backend/internal/app"
	"github.com/spiderocious/devfest-akure-2026/wallet/backend/internal/db"
	"github.com/spiderocious/devfest-akure-2026/wallet/backend/internal/env"
	"github.com/spiderocious/devfest-akure-2026/wallet/backend/internal/features/events"
	"github.com/spiderocious/devfest-akure-2026/wallet/backend/internal/logx"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := env.Load()
	if err != nil {
		return err
	}
	log := logx.New(cfg.LogLevel)
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	engine := app.Engine(app.Deps{Config: cfg, Pool: pool, Log: log, Hub: events.NewHub()})

	// WriteTimeout is zero because /events is a long-lived stream; the SSE
	// handler enforces its own liveness with keep-alive frames.
	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           engine,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      0,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		log.Info("listening", "port", cfg.Port, "env", cfg.Env)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
