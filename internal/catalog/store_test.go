package catalog

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ElHefe3/atlas-bridge/internal/model"
)

func TestSearchAndUpsert(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "catalogue.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	b := model.Book{ProviderID: "anna-local", ExternalID: "md5:abc", Title: "The Public Domain Book", Author: "Example Author", ISBN: "978123", Files: []model.File{{FileID: "epub", Format: "epub"}}}
	if err := s.Upsert(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	got, more, err := s.Search(context.Background(), "public domain", 1, 20, "epub")
	if err != nil {
		t.Fatal(err)
	}
	if more || len(got) != 1 || got[0].ExternalID != b.ExternalID {
		t.Fatalf("unexpected result: %#v, more=%v", got, more)
	}
}
