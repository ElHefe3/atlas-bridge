package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ElHefe3/atlas-bridge/internal/api"
	"github.com/ElHefe3/atlas-bridge/internal/cache"
	"github.com/ElHefe3/atlas-bridge/internal/config"
	"github.com/ElHefe3/atlas-bridge/internal/model"
	"github.com/ElHefe3/atlas-bridge/internal/providers"
	"github.com/ElHefe3/atlas-bridge/internal/safehttp"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "healthcheck" {
		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Get("http://127.0.0.1:8080/healthz")
		if err != nil || resp.StatusCode != http.StatusOK {
			fmt.Fprintln(os.Stderr, "Atlas Bridge is unhealthy")
			os.Exit(1)
		}
		resp.Body.Close()
		return
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg, err := config.Load()
	if err != nil {
		logger.Error("configuration failed", "error", err)
		os.Exit(1)
	}
	store, err := cache.Open(cfg.DataPath, 24*time.Hour)
	if err != nil {
		logger.Error("cache failed", "error", err)
		os.Exit(1)
	}
	defer store.Close()
	annaHTTP, err := safehttp.New(cfg.AnnaOrigins, cfg.RequestTimeout, cfg.DownloadLimit)
	if err != nil {
		logger.Error("Anna HTTP policy failed", "error", err)
		os.Exit(1)
	}
	libgenHTTP, err := safehttp.New(cfg.LibGenOrigins, cfg.RequestTimeout, cfg.DownloadLimit)
	if err != nil {
		logger.Error("LibGen HTTP policy failed", "error", err)
		os.Exit(1)
	}
	registered := []model.Provider{providers.NewAnna(annaHTTP, store, cfg.AnnaMirrors, cfg.AnnaKey), providers.NewLibGen(libgenHTTP, store, cfg.LibGenMirrors)}
	server := &http.Server{Addr: cfg.ListenAddress, Handler: api.New(cfg.BridgeToken, cfg.PublicBaseURL, registered, logger).Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, WriteTimeout: 10 * time.Minute, MaxHeaderBytes: 1 << 20}
	go func() {
		logger.Info("Atlas Bridge listening", "address", cfg.ListenAddress)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server failed", "error", err)
			os.Exit(1)
		}
	}()
	stop, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	<-stop.Done()
	ctx, done := context.WithTimeout(context.Background(), 30*time.Second)
	defer done()
	if err := server.Shutdown(ctx); err != nil {
		logger.Error("shutdown failed", "error", err)
	}
}
