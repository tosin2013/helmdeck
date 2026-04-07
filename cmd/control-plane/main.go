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
	"github.com/tosin2013/helmdeck/internal/session"
	dockerrt "github.com/tosin2013/helmdeck/internal/session/docker"
)

var (
	version = "dev"     // overridden at build time via -ldflags
	commit  = "unknown" // overridden at build time via -ldflags
)

func main() {
	addr := flag.String("addr", envOr("HELMDECK_ADDR", ":3000"), "listen address")
	network := flag.String("session-network", envOr("HELMDECK_SESSION_NETWORK", ""), "Docker network to attach session containers to (default: host default)")
	disableRuntime := flag.Bool("disable-runtime", envBool("HELMDECK_DISABLE_RUNTIME"), "skip Docker session runtime (dev mode; /api/v1/sessions returns 503)")
	watchdogIv := flag.Duration("watchdog-interval", 30*time.Second, "session watchdog scan interval")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)
	logger.Info("helmdeck control-plane starting", "version", version, "commit", commit, "addr", *addr)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var rt session.Runtime
	if !*disableRuntime {
		opts := []dockerrt.Option{}
		if *network != "" {
			opts = append(opts, dockerrt.WithNetwork(*network))
		}
		dr, err := dockerrt.New(opts...)
		if err != nil {
			logger.Warn("docker runtime unavailable; /api/v1/sessions disabled", "err", err)
		} else {
			if err := dr.PruneOrphans(ctx); err != nil {
				logger.Warn("orphan prune failed", "err", err)
			}
			rt = dr
			defer dr.Close()
			wd := session.NewWatchdog(rt, logger, *watchdogIv)
			go wd.Run(ctx)
			logger.Info("session runtime ready", "network", *network, "watchdog_interval", watchdogIv.String())
		}
	} else {
		logger.Info("session runtime disabled by flag")
	}

	srv := &http.Server{
		Addr: *addr,
		Handler: api.NewRouter(api.Deps{
			Logger:  logger,
			Version: version,
			Runtime: rt,
		}),
		ReadHeaderTimeout: 5 * time.Second,
	}

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

func envBool(key string) bool {
	v := os.Getenv(key)
	return v == "1" || v == "true" || v == "yes"
}
