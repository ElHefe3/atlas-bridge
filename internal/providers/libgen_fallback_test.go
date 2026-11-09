package providers

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ElHefe3/atlas-bridge/internal/cache"
	"github.com/ElHefe3/atlas-bridge/internal/model"
)

const fallbackID = "0123456789abcdef0123456789abcdef"

// hostRoutedClient answers by host, standing in for LibGen mirrors and CDN hosts.
type hostRoutedClient struct {
	mu     sync.Mutex
	routes map[string]func(*http.Request) (*http.Response, error)
	calls  []string
}

func (c *hostRoutedClient) Do(req *http.Request) (*http.Response, error) {
	c.mu.Lock()
	c.calls = append(c.calls, req.URL.Host+req.URL.Path)
	route := c.routes[req.URL.Host]
	c.mu.Unlock()
	if route == nil {
		return nil, &url.Error{Op: "Get", URL: req.URL.String(), Err: &net.DNSError{Err: "no such host", Name: req.URL.Host}}
	}
	return route(req)
}

func page(status int, body string, req *http.Request) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"text/html"}},
		Body: io.NopCloser(strings.NewReader(body)), Request: req}
}

// mirror serves a detail page whose GET link points at the given CDN host.
func mirror(cdn string) func(*http.Request) (*http.Response, error) {
	return func(req *http.Request) (*http.Response, error) {
		return page(http.StatusOK, `<a href="https://`+cdn+`/get.php?md5=`+fallbackID+`&key=SECRET">GET</a>`, req), nil
	}
}

func pdf(req *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/pdf"}},
		Body: io.NopCloser(strings.NewReader("%PDF-1.7 book")), Request: req}, nil
}

func newFallbackLibGen(t *testing.T, client *hostRoutedClient, mirrors ...string) *LibGen {
	t.Helper()
	store, err := cache.Open(filepath.Join(t.TempDir(), "cache.db"), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	book := model.Book{ProviderID: "libgen", ExternalID: fallbackID, Title: "Ley Farming",
		Files: []model.File{{FileID: "0-pdf", Format: "pdf", URL: "https://libgen.bz/ads.php?md5=" + fallbackID}}}
	if err := store.Put(book); err != nil {
		t.Fatal(err)
	}
	return &LibGen{client: client, cache: store, mirrors: mirrors}
}

func TestLibGenFallsBackToAnotherMirrorWhenTheCDNTimesOut(t *testing.T) {
	client := &hostRoutedClient{routes: map[string]func(*http.Request) (*http.Response, error){
		"libgen.bz": mirror("cdn3.booksdl.lc"),
		"cdn3.booksdl.lc": func(r *http.Request) (*http.Response, error) {
			return page(522, "<h1>Connection timed out</h1>", r), nil
		},
		"libgen.la":       mirror("cdn4.booksdl.lc"),
		"cdn4.booksdl.lc": pdf,
	}}
	l := newFallbackLibGen(t, client, "https://libgen.gl", "https://libgen.bz", "https://libgen.la")

	file, err := l.OpenFile(context.Background(), fallbackID, "0-pdf")
	if err != nil {
		t.Fatalf("expected fallback to succeed, got %v (calls %v)", err, client.calls)
	}
	file.Body.Close()
	want := []string{"libgen.bz/ads.php", "cdn3.booksdl.lc/get.php", "libgen.gl/ads.php", "libgen.la/ads.php", "cdn4.booksdl.lc/get.php"}
	if strings.Join(client.calls, " ") != strings.Join(want, " ") {
		t.Fatalf("calls = %v, want %v", client.calls, want)
	}
}

func TestLibGenNetworkFailuresBecomeRetryableWithoutLeakingKeys(t *testing.T) {
	client := &hostRoutedClient{routes: map[string]func(*http.Request) (*http.Response, error){
		"libgen.bz": mirror("cdn1.booksdl.lc"), // cdn1 has no route: DNS failure
	}}
	l := newFallbackLibGen(t, client, "https://libgen.bz")

	_, err := l.OpenFile(context.Background(), fallbackID, "0-pdf")
	var pe *model.ProviderError
	if !errors.As(err, &pe) || pe.Code != "upstream_unavailable" || !pe.Retryable || pe.Status != http.StatusServiceUnavailable {
		t.Fatalf("got %#v", err)
	}
	if !strings.Contains(pe.Message, "cdn1.booksdl.lc") || !strings.Contains(pe.Message, "no such host") || strings.Contains(pe.Message, "SECRET") {
		t.Fatalf("message = %q", pe.Message)
	}
}

func TestLibGenStopsAtTheCDNRateLimit(t *testing.T) {
	client := &hostRoutedClient{routes: map[string]func(*http.Request) (*http.Response, error){
		"libgen.bz": mirror("cdn4.booksdl.lc"),
		"libgen.la": mirror("cdn4.booksdl.lc"),
		"cdn4.booksdl.lc": func(r *http.Request) (*http.Response, error) {
			return page(http.StatusInternalServerError, libgenLimitPage, r), nil
		},
	}}
	l := newFallbackLibGen(t, client, "https://libgen.bz", "https://libgen.la")

	_, err := l.OpenFile(context.Background(), fallbackID, "0-pdf")
	var pe *model.ProviderError
	if !errors.As(err, &pe) || pe.Code != "upstream_rate_limited" {
		t.Fatalf("got %v", err)
	}
	if len(client.calls) != 2 {
		t.Fatalf("rate limit must stop fallback; calls = %v", client.calls)
	}
}

func TestLibGenFallbackIsBounded(t *testing.T) {
	client := &hostRoutedClient{routes: map[string]func(*http.Request) (*http.Response, error){}}
	l := newFallbackLibGen(t, client, "https://a.test", "https://b.test", "https://c.test", "https://d.test", "https://e.test")

	if _, err := l.OpenFile(context.Background(), fallbackID, "0-pdf"); err == nil {
		t.Fatal("expected failure")
	}
	if len(client.calls) != maxLibGenDownloadAttempts {
		t.Fatalf("calls = %d (%v), want %d", len(client.calls), client.calls, maxLibGenDownloadAttempts)
	}
}

func TestLibGenDetailCandidatesKeepOrderAndSkipDuplicates(t *testing.T) {
	got := libgenDetailCandidates("https://libgen.bz/ads.php?md5=x", []string{"https://libgen.gl", "https://LIBGEN.bz", "https://libgen.la", "::bad"})
	want := []string{"https://libgen.bz/ads.php?md5=x", "https://libgen.gl/ads.php?md5=x", "https://libgen.la/ads.php?md5=x"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("got %v", got)
	}
}
