package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ElHefe3/atlas-bridge/internal/api"
	"github.com/ElHefe3/atlas-bridge/internal/cache"
	"github.com/ElHefe3/atlas-bridge/internal/catalog"
	"github.com/ElHefe3/atlas-bridge/internal/config"
	"github.com/ElHefe3/atlas-bridge/internal/model"
	"github.com/ElHefe3/atlas-bridge/internal/providers"
	"github.com/ElHefe3/atlas-bridge/internal/safehttp"
	"github.com/ElHefe3/atlas-bridge/internal/torrent"
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
	catalogue, err := catalog.Open(cfg.DataPath + ".catalogue.sqlite")
	if err != nil {
		logger.Error("catalogue failed", "error", err)
		os.Exit(1)
	}
	defer catalogue.Close()
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
	serverAPI := api.NewWithCatalogueAndProviders(cfg.BridgeToken, cfg.PublicBaseURL, registered, logger, catalogue, cfg.DataPath+"-staging", cfg.DownloadLimit)
	server := &http.Server{Addr: cfg.ListenAddress, Handler: serverAPI.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, WriteTimeout: 10 * time.Minute, MaxHeaderBytes: 1 << 20}
	serverAPI.ConfigureTorrentSources(catalogue, torrent.NewTransmission(cfg.TransmissionRPC))
	go syncConfiguredCatalogues(cfg, catalogue, logger)
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

func syncConfiguredCatalogues(cfg config.Config, catalogue *catalog.Store, logger *slog.Logger) {
	ctx := context.Background()
	if cfg.CatalogueFilesTorrent != "" && cfg.CatalogueFilesZstd != "" && cfg.CatalogueFilesTorrentPath != "" {
		go func() {
			filesCfg := cfg
			filesCfg.CatalogueTorrent = cfg.CatalogueFilesTorrent
			filesCfg.CatalogueTorrentPath = cfg.CatalogueFilesTorrentPath
			filesCfg.CatalogueZstd = cfg.CatalogueFilesZstd
			if err := retrieveCatalogueTorrent(ctx, filesCfg, logger); err != nil {
				logger.Error("file catalogue torrent retrieval failed", "error", err)
				return
			}
			logger.Info("file catalogue torrent retrieved", "path", cfg.CatalogueFilesZstd)
			count, skipped, err := ingestFilesOnce(ctx, catalogue, cfg.CatalogueFilesZstd, cfg.CatalogueMaxExpanded)
			if err != nil {
				logger.Error("compressed file catalogue ingest failed", "records", count, "skipped", skipped, "error", err)
			} else {
				logger.Info("compressed file catalogue ingested", "records", count, "skipped", skipped)
			}
		}()
	}
	if cfg.CatalogueTorrent != "" && cfg.CatalogueZstd != "" && cfg.CatalogueTorrentPath != "" {
		if err := retrieveCatalogueTorrent(ctx, cfg, logger); err != nil {
			logger.Error("catalogue torrent retrieval failed", "error", err)
		} else {
			logger.Info("catalogue torrent retrieved", "path", cfg.CatalogueZstd)
		}
	}
	if cfg.CatalogueJSONL != "" {
		if input, err := os.Open(cfg.CatalogueJSONL); err == nil {
			count, ingestErr := catalogue.IngestJSONL(ctx, input, 0)
			_ = input.Close()
			if ingestErr != nil {
				logger.Error("catalogue ingest failed", "records", count, "error", ingestErr)
			} else {
				logger.Info("catalogue ingested", "records", count)
			}
		} else {
			logger.Error("catalogue ingest failed", "error", err)
		}
	}
	if cfg.CatalogueZstd != "" {
		count, skipped, err := ingestRecordsOnce(ctx, catalogue, cfg.CatalogueZstd, cfg.CatalogueMaxExpanded)
		if err != nil {
			logger.Error("compressed catalogue ingest failed", "records", count, "skipped", skipped, "error", err)
		} else {
			logger.Info("compressed catalogue ingested", "records", count, "skipped", skipped)
		}
	}
}

func ingestRecordsOnce(ctx context.Context, catalogue *catalog.Store, path string, max int64) (int, int, error) {
	return ingestOnce(path, func() (int, int, error) { return catalogue.IngestZstdJSONL(ctx, path, 0, max) })
}
func ingestFilesOnce(ctx context.Context, catalogue *catalog.Store, path string, max int64) (int, int, error) {
	return ingestOnce(path, func() (int, int, error) { return catalogue.IngestZstdFilesJSONL(ctx, path, 0, max) })
}
func ingestOnce(path string, fn func() (int, int, error)) (int, int, error) {
	marker := path + ".ingested"
	if _, err := os.Stat(marker); err == nil {
		return 0, 0, nil
	}
	count, skipped, err := fn()
	if err == nil {
		_ = os.WriteFile(marker, []byte("complete\n"), 0o600)
	}
	return count, skipped, err
}

func retrieveCatalogueTorrent(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	rel := filepath.Clean(filepath.FromSlash(cfg.CatalogueTorrentPath))
	if rel == "." || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("catalogue torrent path must be relative and contained")
	}
	if err := os.MkdirAll(filepath.Dir(cfg.CatalogueZstd), 0o700); err != nil {
		return err
	}
	root := filepath.Join(filepath.Dir(cfg.CatalogueZstd), "torrents")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	dir := root
	client := torrent.NewTransmission(cfg.TransmissionRPC)
	transmissionDir := "/downloads"
	f, size, err := client.DownloadFile(ctx, torrent.AddRequest{Metainfo: cfg.CatalogueTorrent, DownloadDir: transmissionDir}, filepath.Join(dir, rel), cfg.CataloguePayloadLimit)
	if err != nil {
		return err
	}
	defer f.Close()
	tmp := cfg.CatalogueZstd + ".part"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, f)
	syncErr := out.Sync()
	closeErr := out.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil {
		_ = os.Remove(tmp)
		if copyErr != nil {
			return copyErr
		}
		return fmt.Errorf("catalogue staging failed")
	}
	if size <= 0 {
		_ = os.Remove(tmp)
		return fmt.Errorf("catalogue torrent returned an empty file")
	}
	if err := os.Rename(tmp, cfg.CatalogueZstd); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	logger.Info("catalogue payload staged", "bytes", size)
	return nil
}
