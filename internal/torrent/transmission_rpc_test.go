package torrent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestDownloadFileSelectsOnlyRequestedTorrentEntry(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "folder", "book.epub")
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("PK\x03\x04fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	var wanted, unwanted []int
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
			arguments["torrent-added"] = map[string]any{"id": 7, "name": "folder", "hashString": "fixture"}
		case "torrent-get":
			fields, _ := call.Arguments["fields"].([]any)
			if len(fields) == 1 && fields[0] == "files" {
				arguments["torrents"] = []any{map[string]any{"files": []any{
					map[string]any{"name": "folder/other.pdf", "length": 999},
					map[string]any{"name": "folder/book.epub", "length": 11},
				}}}
			} else {
				arguments["torrents"] = []any{map[string]any{"id": 7, "percentDone": 1, "sizeWhenDone": 11}}
			}
		case "torrent-set":
			wanted = numbers(call.Arguments["files-wanted"])
			unwanted = numbers(call.Arguments["files-unwanted"])
		case "torrent-start", "torrent-remove":
		default:
			t.Errorf("unexpected RPC method %q", call.Method)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": "success", "arguments": arguments})
	}))
	defer server.Close()
	client := NewTransmission(server.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	file, size, err := client.DownloadFile(ctx, AddRequest{Metainfo: "https://example.test/data.torrent", DownloadDir: "/downloads/job"}, target, 1024)
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
	if size != 11 || !reflect.DeepEqual(wanted, []int{1}) || !reflect.DeepEqual(unwanted, []int{0}) {
		t.Fatalf("size=%d wanted=%v unwanted=%v", size, wanted, unwanted)
	}
}

func numbers(value any) []int {
	values, _ := value.([]any)
	out := make([]int, 0, len(values))
	for _, value := range values {
		if number, ok := value.(float64); ok {
			out = append(out, int(number))
		}
	}
	return out
}
