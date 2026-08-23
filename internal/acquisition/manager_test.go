package acquisition

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ElHefe3/atlas-bridge/internal/catalog"
	"github.com/ElHefe3/atlas-bridge/internal/model"
	"github.com/ElHefe3/atlas-bridge/internal/torrent"
)

func TestCompletedFileIsConsumedAndRemoved(t *testing.T) {
	m := NewManager()
	path := filepath.Join(t.TempDir(), "completed.part")
	if err := os.WriteFile(path, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	m.files["job"] = path
	file, ok := m.File("job")
	if !ok {
		t.Fatal("completed file was not returned")
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("completed file was not removed: %v", err)
	}
	if _, ok = m.File("job"); ok {
		t.Fatal("completed file was consumable more than once")
	}
}

func minimalEPUB(t *testing.T) []byte {
	t.Helper()
	var data bytes.Buffer
	archive := zip.NewWriter(&data)
	mimetype, err := archive.CreateHeader(&zip.FileHeader{Name: "mimetype", Method: zip.Store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = mimetype.Write([]byte("application/epub+zip")); err != nil {
		t.Fatal(err)
	}
	container, err := archive.Create("META-INF/container.xml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = container.Write([]byte(`<?xml version="1.0"?><container/>`)); err != nil {
		t.Fatal(err)
	}
	if err = archive.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func TestEPUBValidationRequiresReadableStructure(t *testing.T) {
	valid := filepath.Join(t.TempDir(), "valid.epub")
	if err := os.WriteFile(valid, minimalEPUB(t), 0o600); err != nil {
		t.Fatal(err)
	}
	if !validSignature(valid, "epub") {
		t.Fatal("valid EPUB was rejected")
	}
	invalid := filepath.Join(t.TempDir(), "invalid.epub")
	if err := os.WriteFile(invalid, []byte("PK\x03\x04not-a-zip"), 0o600); err != nil {
		t.Fatal(err)
	}
	if validSignature(invalid, "epub") {
		t.Fatal("invalid EPUB was accepted")
	}
}

type torrentOnlyProvider struct{}

func (torrentOnlyProvider) Info() model.ProviderInfo {
	return model.ProviderInfo{ID: "anna-local", Name: "Local catalogue", SearchAvailable: true, DownloadConfigured: true}
}
func (torrentOnlyProvider) Search(context.Context, string, model.SearchOptions) (model.SearchResponse, error) {
	return model.SearchResponse{}, nil
}
func (torrentOnlyProvider) Details(context.Context, string) (model.Book, error) {
	return model.Book{}, nil
}
func (torrentOnlyProvider) OpenCover(context.Context, string) (*model.RemoteFile, error) {
	return nil, errors.New("not available")
}
func (torrentOnlyProvider) OpenFile(context.Context, string, string) (*model.RemoteFile, error) {
	return nil, errors.New("direct fallback should not be used")
}

func TestTorrentAcquisitionCreatesStagingAndCompletes(t *testing.T) {
	root := t.TempDir()
	store, err := catalog.Open(filepath.Join(root, "catalogue.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.PutLocator(context.Background(), catalog.Locator{
		ProviderID: "anna-local", ExternalID: "book", FileID: "epub", Kind: catalog.LocatorTorrentFile,
		Metainfo: "https://example.test/data.torrent", Path: "folder/book.epub",
	}); err != nil {
		t.Fatal(err)
	}

	var downloadDir string
	epub := minimalEPUB(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var call struct {
			Method    string         `json:"method"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.NewDecoder(r.Body).Decode(&call); err != nil {
			t.Error(err)
			return
		}
		arguments := map[string]any{}
		switch call.Method {
		case "torrent-add":
			downloadDir, _ = call.Arguments["download-dir"].(string)
			arguments["torrent-added"] = map[string]any{"id": 9, "name": "folder", "hashString": "fixture"}
		case "torrent-get":
			fields, _ := call.Arguments["fields"].([]any)
			if len(fields) == 1 && fields[0] == "files" {
				arguments["torrents"] = []any{map[string]any{"files": []any{map[string]any{"name": "folder/book.epub", "length": len(epub)}}}}
			} else {
				arguments["torrents"] = []any{map[string]any{"id": 9, "percentDone": 1, "sizeWhenDone": len(epub), "downloadedEver": len(epub)}}
			}
		case "torrent-start":
			jobDir := filepath.Base(filepath.ToSlash(downloadDir))
			path := filepath.Join(root, "torrents", jobDir, "folder", "book.epub")
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Error(err)
			} else if err := os.WriteFile(path, epub, 0o600); err != nil {
				t.Error(err)
			}
		case "torrent-set", "torrent-remove":
		default:
			t.Errorf("unexpected RPC method %q", call.Method)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": "success", "arguments": arguments})
	}))
	defer server.Close()

	m := NewManagerWithProviders([]model.Provider{torrentOnlyProvider{}}, filepath.Join(root, "staging"), 1024)
	m.SetTorrentSources(store, torrent.NewTransmission(server.URL))
	job, err := m.Create(context.Background(), model.AcquisitionRequest{ProviderID: "anna-local", ExternalID: "book", FileID: "epub"})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status, _ := m.Get(job.ID)
		if status.Status == model.AcquisitionCompleted {
			file, ok := m.File(job.ID)
			if !ok {
				t.Fatal("completed acquisition had no file")
			}
			data, readErr := io.ReadAll(file)
			closeErr := file.Close()
			if readErr != nil || closeErr != nil || !bytes.Equal(data, epub) {
				t.Fatalf("read=%v close=%v bytes=%d", readErr, closeErr, len(data))
			}
			return
		}
		if status.Status == model.AcquisitionFailed {
			t.Fatalf("acquisition failed: %s", status.Error)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("acquisition did not complete")
}
