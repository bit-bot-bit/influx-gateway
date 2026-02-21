package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"

	"influx-bouncer/internal/config"
	"influx-bouncer/internal/gateway"
	"influx-bouncer/internal/telemetry"
)

func main() {
	// Initialize structured logger
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	cfg, err := config.LoadConfig()
	if err != nil {
		slog.Error("load config failed", "err", err)
		os.Exit(1)
	}

	// Initialize OpenTelemetry
	shutdown, err := telemetry.InitTracer(context.Background(), "influx-gateway")
	if err != nil {
		slog.Error("failed to init tracer", "err", err)
		// Proceed without tracing or exit? Usually proceed.
	} else {
		defer func() {
			if err := shutdown(context.Background()); err != nil {
				slog.Error("failed to shutdown tracer", "err", err)
			}
		}()
	}

	srv := gateway.NewServer(cfg)
	slog.Info("influx-gateway listening", "addr", cfg.HTTPAddr)
	if err := http.ListenAndServe(cfg.HTTPAddr, srv.Handler()); err != nil {
		slog.Error("server failed", "err", err)
		os.Exit(1)
	}
}
