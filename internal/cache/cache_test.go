package cache

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/ElHefe3/atlas-bridge/internal/model"
)

func TestStoreRoundTripAndExpiry(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "cache.db"), 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	book := model.Book{ProviderID: "test", ExternalID: "1", Title: "Public Domain Book", Files: []model.File{}}
	if err := store.Put(book); err != nil {
		t.Fatal(err)
	}
	got, ok, err := store.Get("test", "1")
	if err != nil || !ok || got.Title != book.Title {
		t.Fatalf("unexpected cache result: %#v, %v, %v", got, ok, err)
	}
	time.Sleep(30 * time.Millisecond)
	_, ok, err = store.Get("test", "1")
	if err != nil || ok {
		t.Fatalf("expired entry remained: ok=%v err=%v", ok, err)
	}
}
