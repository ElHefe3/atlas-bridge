//go:build live

// Live LibGen download check against the real mirrors, using the exact environment
// production is installed with (deploy/atlas-bridge.container). It prints the real
// error for each book, which the API reports only as internal_error.
//
//	go test -tags live -run TestLiveLibGenDownloads -count=1 -v ./internal/providers/
//	ATLAS_LIVE_QUERY="ley farming" ATLAS_LIVE_BOOKS=8 go test -tags live ...
//
// Only the first bytes of each file are read, to check the signature; nothing is saved.
package providers

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ElHefe3/atlas-bridge/internal/cache"
	"github.com/ElHefe3/atlas-bridge/internal/config"
	"github.com/ElHefe3/atlas-bridge/internal/model"
	"github.com/ElHefe3/atlas-bridge/internal/safehttp"
)

func TestLiveLibGenDownloads(t *testing.T) {
	loadProductionEnvironment(t, filepath.Join("..", "..", "deploy", "atlas-bridge.container"))
	token := filepath.Join(t.TempDir(), "bridge-token")
	if err := os.WriteFile(token, []byte(strings.Repeat("x", 64)), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ATLAS_BRIDGE_TOKEN_FILE", token)
	t.Setenv("ATLAS_ANNA_KEY_FILE", filepath.Join(t.TempDir(), "absent"))
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	client, err := safehttp.New(cfg.LibGenOrigins, cfg.RequestTimeout, cfg.DownloadLimit)
	if err != nil {
		t.Fatal(err)
	}
	store, err := cache.Open(filepath.Join(t.TempDir(), "cache.db"), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	libgen := NewLibGen(client, store, cfg.LibGenMirrors)

	query := envOr("ATLAS_LIVE_QUERY", "Mechanized Farming")
	limit, _ := strconv.Atoi(envOr("ATLAS_LIVE_BOOKS", "5"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	found, err := libgen.Search(ctx, query, model.SearchOptions{Page: 1, PageSize: 25})
	if err != nil {
		t.Fatalf("search %q: %v", query, err)
	}
	fmt.Printf("allowed LibGen origins: %s\nsearch %q: %d results\n", strings.Join(cfg.LibGenOrigins, " "), query, len(found.Results))

	failures := 0
	for i, book := range found.Results {
		if i >= limit {
			break
		}
		details, err := libgen.Details(ctx, book.ExternalID)
		if err != nil || len(details.Files) == 0 {
			fmt.Printf("  %-32s details: %v\n", book.ExternalID, err)
			failures++
			continue
		}
		file := details.Files[0]
		started := time.Now()
		remote, err := libgen.OpenFile(ctx, book.ExternalID, file.FileID)
		if err != nil {
			fmt.Printf("  FAIL %s %-5s %s\n       %s\n", book.ExternalID, file.Format, title(details.Title), describe(err))
			failures++
			continue
		}
		head, _ := bufio.NewReader(remote.Body).Peek(5)
		_, _ = io.CopyN(io.Discard, remote.Body, 0)
		remote.Body.Close()
		fmt.Printf("  OK   %s %-5s %s (%s, %q, %.1fs)\n", book.ExternalID, file.Format, title(details.Title), host(remote.URL), head, time.Since(started).Seconds())
	}
	if failures > 0 {
		t.Errorf("%d of the first %d books could not be opened", failures, min(limit, len(found.Results)))
	}
}

// loadProductionEnvironment applies the Environment= lines of the production Quadlet.
func loadProductionEnvironment(t *testing.T, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if value, ok := strings.CutPrefix(line, "Environment="); ok {
			if name, setting, ok := strings.Cut(value, "="); ok {
				t.Setenv(name, setting)
			}
		}
	}
}

func describe(err error) string {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return fmt.Sprintf("%s %s: %v", urlErr.Op, host(urlErr.URL), urlErr.Err)
	}
	return fmt.Sprintf("%T: %v", err, err)
}

func host(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return u.Host
	}
	return raw
}

func title(s string) string {
	s = strings.TrimSpace(strings.TrimRight(s, "| "))
	if len(s) > 48 {
		return s[:48] + "…"
	}
	return s
}

func envOr(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return fallback
}
