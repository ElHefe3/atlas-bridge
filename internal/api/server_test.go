package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ElHefe3/atlas-bridge/internal/model"
)

const testToken = "0123456789abcdef0123456789abcdef"

type fakeProvider struct{}

func (fakeProvider) Info() model.ProviderInfo {
	return model.ProviderInfo{ID: "fake", Name: "Fake", SearchAvailable: true, DownloadConfigured: true}
}
func (fakeProvider) Search(_ context.Context, q string, o model.SearchOptions) (model.SearchResponse, error) {
	return model.SearchResponse{ProviderID: "fake", Query: q, Page: o.Page, Results: []model.Book{{ProviderID: "fake", ExternalID: "abc", Title: "Pride and Prejudice", CoverURL: "https://upstream.invalid/c.jpg", Files: []model.File{{FileID: "epub", Format: "epub", URL: "https://upstream.invalid/file"}}}}}, nil
}
func (fakeProvider) Details(context.Context, string) (model.Book, error) {
	return model.Book{ProviderID: "fake", ExternalID: "abc", Title: "Pride and Prejudice", Files: []model.File{}}, nil
}
func (fakeProvider) OpenCover(context.Context, string) (*model.RemoteFile, error) {
	return &model.RemoteFile{ContentType: "image/png", Body: io.NopCloser(strings.NewReader("image"))}, nil
}
func (fakeProvider) OpenFile(context.Context, string, string) (*model.RemoteFile, error) {
	return &model.RemoteFile{ContentType: "application/epub+zip", Body: io.NopCloser(strings.NewReader("book"))}, nil
}

func TestAuthenticationAndDecoratedSearch(t *testing.T) {
	server := New(testToken, "http://atlas-bridge:8080", []model.Provider{fakeProvider{}}, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
	unauthorized := httptest.NewRecorder()
	server.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/v1/providers", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("got %d", unauthorized.Code)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/providers/fake/search?q=pride&page=2", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("got %d: %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	if strings.Contains(body, "upstream.invalid") {
		t.Fatal("upstream URL leaked")
	}
	if !strings.Contains(body, "http://atlas-bridge:8080/v1/providers/fake/books/abc/cover") || !strings.Contains(body, "/files/epub") {
		t.Fatalf("proxy URLs missing: %s", body)
	}
}

func TestHealthDoesNotRequireAuthentication(t *testing.T) {
	server := New(testToken, "http://bridge", []model.Provider{fakeProvider{}}, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
	response := httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("got %d", response.Code)
	}
}
