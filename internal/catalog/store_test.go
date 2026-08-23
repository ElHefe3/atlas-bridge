package catalog

import (
	"bytes"
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

func TestZlib3FileMapResolvesBookFileBySourceID(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "catalogue.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	book := model.Book{ProviderID: "anna-local", ExternalID: "aacid__zlib3_records__20240809T171652Z__22430000__abc", Title: "Public Domain", Files: []model.File{{FileID: "epub", Format: "epub"}}}
	if err = s.Upsert(ctx, book); err != nil {
		t.Fatal(err)
	}
	fixture := []byte("{\"aacid\":\"aacid__zlib3_files__x__22430000__def\",\"data_folder\":\"fixture\",\"metadata\":{\"zlibrary_id\":\"22430000\",\"md5\":\"21f19f95c4b969d06fe5860a98e29f0d\"}}\n")
	if count, skipped, ingestErr := s.SyncAnnaFilesJSONL(ctx, bytes.NewReader(fixture), 0); ingestErr != nil || count != 1 || skipped != 0 {
		t.Fatalf("ingest count=%d skipped=%d err=%v", count, skipped, ingestErr)
	}
	file, ok, err := s.GetFile(ctx, "anna-local", book.ExternalID, "epub")
	if err != nil || !ok || file.MD5 != "21f19f95c4b969d06fe5860a98e29f0d" {
		t.Fatalf("file=%#v ok=%v err=%v", file, ok, err)
	}
}

func TestDecodeZlib3RecordAliases(t *testing.T) {
	r, err := decodeAnnaRecord([]byte(`{"aacid":"aacid__zlib3_records__x__1__abc","metadata":{"extension":"epub","filesize_reported":42,"md5_reported":"21f19f95c4b969d06fe5860a98e29f0d","title":"Pride and Prejudice","author":"Jane Austen","cover_path":"/covers/books/21/f1/9f/example.jpg","isbns":["9780000000000"]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if r.MD5 == "" || r.Format != "epub" || r.Size == nil || *r.Size != 42 || r.CoverURL == "" || r.ISBN != "9780000000000" {
		t.Fatalf("unexpected record: %#v", r)
	}
}
