package api

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ElHefe3/atlas-bridge/internal/model"
)

// refusedRedirectProvider fails OpenFile the way production did when LibGen moved
// downloads to an origin that was not allowed: a plain *url.Error, not a ProviderError.
type refusedRedirectProvider struct{ fakeProvider }

func (refusedRedirectProvider) Info() model.ProviderInfo {
	return model.ProviderInfo{ID: "libgen", Name: "LibGen", SearchAvailable: true, DownloadConfigured: true}
}

func (refusedRedirectProvider) OpenFile(context.Context, string, string) (*model.RemoteFile, error) {
	return nil, &url.Error{Op: "Get", URL: "https://cdn4.booksdl.lc/get.php?md5=59613a14&key=SECRETKEY123",
		Err: errors.New("origin cdn4.booksdl.lc is not allowed")}
}

func TestInternalErrorLogsRedactedCauseButNotToClient(t *testing.T) {
	var logs bytes.Buffer
	server := New(testToken, "http://bridge", []model.Provider{refusedRedirectProvider{}},
		slog.New(slog.NewTextHandler(&logs, nil))).Handler()
	req := httptest.NewRequest(http.MethodGet, "/v1/providers/libgen/books/59613a14/files/0-pdf", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	res := httptest.NewRecorder()

	server.ServeHTTP(res, req)

	if res.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", res.Code)
	}
	body := res.Body.String()
	if !strings.Contains(body, `"code":"internal_error"`) || strings.Contains(body, "cdn4") {
		t.Fatalf("client response must stay generic, got %s", body)
	}
	logged := logs.String()
	for _, want := range []string{"code=internal_error", "provider=libgen", "origin cdn4.booksdl.lc is not allowed", "https://cdn4.booksdl.lc/get.php?[redacted]"} {
		if !strings.Contains(logged, want) {
			t.Errorf("log is missing %q:\n%s", want, logged)
		}
	}
	if strings.Contains(logged, "SECRETKEY123") || strings.Contains(logged, "md5=") {
		t.Fatalf("log leaked a URL query:\n%s", logged)
	}
}

func TestLogSafeCauseRedactsEveryURLQueryAndBoundsLength(t *testing.T) {
	err := errors.New(`a "https://annas-archive.gl/dyn/api/fast_download.json?md5=x&key=MEMBER" b https://x.test/p?token=T c`)
	got := logSafeCause(err)
	if strings.Contains(got, "MEMBER") || strings.Contains(got, "token=T") {
		t.Fatalf("query not redacted: %s", got)
	}
	if !strings.Contains(got, "https://annas-archive.gl/dyn/api/fast_download.json?[redacted]") || !strings.Contains(got, "https://x.test/p?[redacted]") {
		t.Fatalf("URLs not kept with redacted queries: %s", got)
	}
	long := logSafeCause(errors.New(strings.Repeat("x", 1000)))
	if len(long) > maxLoggedCause+len("…") {
		t.Fatalf("cause not bounded: %d bytes", len(long))
	}
	if logSafeCause(nil) != "" {
		t.Fatal("nil error should log empty cause")
	}
}
