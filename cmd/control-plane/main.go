// Command control-plane is the helmdeck Golang control plane.
//
// It serves the REST API, the embedded React UI, the AI gateway, the MCP
// registry, the credential vault, and orchestrates ephemeral browser session
// containers via the SessionRuntime interface (see ADR 002, 009).
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/tosin2013/helmdeck/internal/api"
)

var (
	version = "dev"     // overridden at build time via -ldflags
	commit  = "unknown" // overridden at build time via -ldflags
)

func main() {
	addr := flag.String("addr", envOr("HELMDECK_ADDR", ":3000"), "listen address")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)
	logger.Info("helmdeck control-plane starting", "version", version, "commit", commit, "addr", *addr)

	srv := &http.Server{
		Addr:              *addr,
		Handler:           api.NewRouter(logger, version),
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server failed", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	logger.Info("shutdown signal received")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "err", err)
		os.Exit(1)
	}
	logger.Info("control-plane stopped cleanly")
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
