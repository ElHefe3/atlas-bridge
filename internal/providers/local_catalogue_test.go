package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/ElHefe3/atlas-bridge/internal/catalog"
	"github.com/ElHefe3/atlas-bridge/internal/model"
)

func TestLocalCatalogueOnlyAdvertisesResolvableFiles(t *testing.T) {
	store, err := catalog.Open(filepath.Join(t.TempDir(), "catalogue.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	available := model.Book{ProviderID: "anna-local", ExternalID: "aacid__zlib3_records__x__42__abc", Title: "Available", Files: []model.File{{FileID: "epub", Format: "epub"}}}
	unavailable := model.Book{ProviderID: "anna-local", ExternalID: "aacid__zlib3_records__x__43__abc", Title: "Unavailable", Files: []model.File{{FileID: "pdf", Format: "pdf"}}}
	if err = store.Upsert(ctx, available); err != nil {
		t.Fatal(err)
	}
	if err = store.Upsert(ctx, unavailable); err != nil {
		t.Fatal(err)
	}
	fixture := bytes.NewBufferString(`{"aacid":"aacid__zlib3_files__x__42__def","metadata":{"zlibrary_id":"42","md5":"21f19f95c4b969d06fe5860a98e29f0d"}}` + "\n")
	if _, _, err = store.SyncAnnaFilesJSONL(ctx, fixture, 0); err != nil {
		t.Fatal(err)
	}
	p := LocalCatalogue{Store: store, Key: "configured"}
	got, err := p.Details(ctx, available.ExternalID)
	if err != nil || len(got.Files) != 1 || got.CoverURL == "" {
		t.Fatalf("available details=%#v err=%v", got, err)
	}
	missing, err := p.Details(ctx, unavailable.ExternalID)
	if err != nil || len(missing.Files) != 0 {
		t.Fatalf("unavailable details=%#v err=%v", missing, err)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("21f19f95c4b969d06fe5860a98e29f0d")) || bytes.Contains(encoded, []byte(`"md5":`)) || bytes.Contains(encoded, []byte(`"aacid":`)) {
		t.Fatalf("internal locator data leaked in public model: %s", encoded)
	}
}

type localCoverClient struct{ calls int }

func (c *localCoverClient) Do(req *http.Request) (*http.Response, error) {
	c.calls++
	body := `{"docs":[{"cover_i":123} ]}`
	contentType := "application/json"
	if req.URL.Host == "covers.openlibrary.org" {
		body = "jpeg-fixture"
		contentType = "image/jpeg"
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(bytes.NewBufferString(body)), Request: req}, nil
}

func TestLocalCatalogueEnrichesAndPersistsCover(t *testing.T) {
	store, err := catalog.Open(filepath.Join(t.TempDir(), "catalogue.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	book := model.Book{ProviderID: "anna-local", ExternalID: "fixture", Title: "Pride and Prejudice", Author: "Jane Austen"}
	if err = store.Upsert(ctx, book); err != nil {
		t.Fatal(err)
	}
	client := &localCoverClient{}
	p := LocalCatalogue{Store: store, MetadataClient: client}
	cover, err := p.OpenCover(ctx, book.ExternalID)
	if err != nil {
		t.Fatal(err)
	}
	cover.Body.Close()
	stored, ok, err := store.GetBook(ctx, "anna-local", book.ExternalID)
	if err != nil || !ok || stored.CoverURL != "https://covers.openlibrary.org/b/id/123-L.jpg" || client.calls != 2 {
		t.Fatalf("stored=%#v ok=%v calls=%d err=%v", stored, ok, client.calls, err)
	}
}
