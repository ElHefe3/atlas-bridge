package torrent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Transmission is the narrow RPC boundary used by Bridge acquisition workers.
// It is deliberately independent of provider manifests and never accepts an
// arbitrary magnet from an HTTP caller without a validated acquisition job.
type Transmission struct {
	endpoint string
	client   *http.Client
}

type AddRequest struct {
	Metainfo    string `json:"metainfo"`
	Filename    string `json:"filename,omitempty"`
	DownloadDir string `json:"download-dir"`
	FilesWanted []int  `json:"files-wanted,omitempty"`
	Paused      bool   `json:"paused"`
}
type AddResponse struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	Hash      string `json:"hashString"`
	Duplicate bool   `json:"-"`
}
type Status struct {
	ID             int     `json:"id"`
	Name           string  `json:"name"`
	Status         int     `json:"status"`
	PercentDone    float64 `json:"percentDone"`
	TotalSize      int64   `json:"totalSize"`
	SizeWhenDone   int64   `json:"sizeWhenDone"`
	DownloadedEver int64   `json:"downloadedEver"`
	Error          string  `json:"errorString"`
}

type File struct {
	Name           string `json:"name"`
	Length         int64  `json:"length"`
	BytesCompleted int64  `json:"bytesCompleted"`
}

func (t *Transmission) FindByName(ctx context.Context, name string) (Status, bool, error) {
	var out struct {
		Torrents []Status `json:"torrents"`
	}
	err := t.call(ctx, "torrent-get", map[string]any{"fields": []string{"id", "name", "status", "percentDone", "totalSize", "sizeWhenDone", "downloadedEver", "errorString"}}, &out)
	if err != nil {
		return Status{}, false, err
	}
	for _, item := range out.Torrents {
		if item.Name == name {
			return item, true, nil
		}
	}
	return Status{}, false, nil
}

func NewTransmission(endpoint string) *Transmission {
	return &Transmission{endpoint: endpoint, client: &http.Client{Timeout: 15 * time.Second}}
}

func (t *Transmission) call(ctx context.Context, method string, args any, out any) error {
	body, err := json.Marshal(map[string]any{"method": method, "arguments": args})
	if err != nil {
		return err
	}
	var resp *http.Response
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpoint, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		if attempt == 1 && resp != nil {
			req.Header.Set("X-Transmission-Session-Id", resp.Header.Get("X-Transmission-Session-Id"))
		}
		resp, err = t.client.Do(req)
		if err != nil {
			return err
		}
		if resp.StatusCode != http.StatusConflict {
			break
		}
		_ = resp.Body.Close()
	}
	if resp == nil {
		return fmt.Errorf("transmission returned no response")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusConflict {
		return fmt.Errorf("transmission session id required")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("transmission returned HTTP %s", resp.Status)
	}
	var envelope struct {
		Result    string          `json:"result"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return err
	}
	if envelope.Result != "success" {
		return fmt.Errorf("transmission RPC failed: %s", envelope.Result)
	}
	if out != nil && len(envelope.Arguments) > 0 {
		return json.Unmarshal(envelope.Arguments, out)
	}
	return nil
}

func (t *Transmission) Add(ctx context.Context, req AddRequest) (AddResponse, error) {
	if strings.HasPrefix(req.Metainfo, "http://") || strings.HasPrefix(req.Metainfo, "https://") || strings.HasPrefix(req.Metainfo, "magnet:") {
		req.Filename = req.Metainfo
		req.Metainfo = ""
	}
	var out struct {
		TorrentAdd       AddResponse `json:"torrent-added"`
		TorrentDuplicate AddResponse `json:"torrent-duplicate"`
	}
	err := t.call(ctx, "torrent-add", req, &out)
	if out.TorrentAdd.ID == 0 && out.TorrentDuplicate.ID != 0 {
		out.TorrentAdd = out.TorrentDuplicate
		out.TorrentAdd.Duplicate = true
	}
	if err == nil && out.TorrentAdd.ID == 0 {
		err = fmt.Errorf("transmission returned no torrent id")
	}
	return out.TorrentAdd, err
}
func (t *Transmission) Get(ctx context.Context, id int) (Status, error) {
	var out struct {
		Torrents []Status `json:"torrents"`
	}
	err := t.call(ctx, "torrent-get", map[string]any{"ids": []int{id}, "fields": []string{"id", "name", "status", "percentDone", "totalSize", "sizeWhenDone", "downloadedEver", "errorString"}}, &out)
	if err != nil {
		return Status{}, err
	}
	if len(out.Torrents) == 0 {
		return Status{}, fmt.Errorf("torrent %s not found", strconv.Itoa(id))
	}
	return out.Torrents[0], nil
}

func (t *Transmission) Files(ctx context.Context, id int) ([]File, error) {
	var out struct {
		Torrents []struct {
			Files []File `json:"files"`
		} `json:"torrents"`
	}
	if err := t.call(ctx, "torrent-get", map[string]any{"ids": []int{id}, "fields": []string{"files"}}, &out); err != nil {
		return nil, err
	}
	if len(out.Torrents) == 0 {
		return nil, fmt.Errorf("torrent %s not found", strconv.Itoa(id))
	}
	return out.Torrents[0].Files, nil
}

func (t *Transmission) SelectFile(ctx context.Context, id, wanted int, total int) error {
	unwanted := make([]int, 0, total-1)
	for i := 0; i < total; i++ {
		if i != wanted {
			unwanted = append(unwanted, i)
		}
	}
	return t.call(ctx, "torrent-set", map[string]any{"ids": []int{id}, "files-wanted": []int{wanted}, "files-unwanted": unwanted}, nil)
}

func (t *Transmission) Start(ctx context.Context, id int) error {
	return t.call(ctx, "torrent-start", map[string]any{"ids": []int{id}}, nil)
}
func (t *Transmission) Remove(ctx context.Context, id int, deleteData bool) error {
	return t.call(ctx, "torrent-remove", map[string]any{"ids": []int{id}, "delete-local-data": deleteData}, nil)
}

// DownloadFile queues a validated torrent and waits for the requested file to
// complete. The caller must provide a path inside the Bridge staging root.
func (t *Transmission) DownloadFile(ctx context.Context, req AddRequest, path string, maxBytes int64) (*os.File, int64, error) {
	if !filepath.IsAbs(path) {
		return nil, 0, fmt.Errorf("staging path must be absolute")
	}
	wantedPath := filepath.ToSlash(strings.TrimPrefix(filepath.Clean(path), filepath.VolumeName(path)))
	if req.DownloadDir != "" {
		// The caller's host path ends with the same torrent-relative path as
		// the Transmission download. Locate it by suffix without trusting a
		// public request to supply file indexes.
		wantedPath = filepath.ToSlash(path)
	}
	req.Paused = true
	added, err := t.Add(ctx, req)
	addedID := added.ID
	owned := !added.Duplicate
	if err != nil {
		return nil, 0, err
	}
	if owned {
		defer t.Remove(context.Background(), addedID, true)
	}
	var files []File
	for len(files) == 0 {
		files, err = t.Files(ctx, addedID)
		if err != nil {
			return nil, 0, err
		}
		if len(files) == 0 {
			select {
			case <-ctx.Done():
				return nil, 0, ctx.Err()
			case <-time.After(time.Second):
			}
		}
	}
	wanted := -1
	for i, file := range files {
		name := filepath.ToSlash(filepath.Clean(file.Name))
		if name == wantedPath || strings.HasSuffix(wantedPath, "/"+name) || strings.HasSuffix(name, "/"+filepath.Base(wantedPath)) {
			if wanted != -1 {
				return nil, 0, fmt.Errorf("requested torrent path is ambiguous")
			}
			wanted = i
			if maxBytes > 0 && file.Length > maxBytes {
				return nil, 0, fmt.Errorf("download exceeds configured size limit")
			}
		}
	}
	if wanted < 0 {
		return nil, 0, fmt.Errorf("requested file is not present in torrent")
	}
	if err = t.SelectFile(ctx, addedID, wanted, len(files)); err != nil {
		return nil, 0, err
	}
	if err = t.Start(ctx, addedID); err != nil {
		return nil, 0, err
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		status, err := t.Get(ctx, addedID)
		if err != nil {
			return nil, 0, err
		}
		if status.SizeWhenDone > maxBytes && maxBytes > 0 {
			return nil, 0, fmt.Errorf("torrent exceeds configured size limit")
		}
		if status.Error != "" {
			return nil, 0, fmt.Errorf("transmission: %s", status.Error)
		}
		if status.PercentDone >= 1 {
			break
		}
		select {
		case <-ctx.Done():
			return nil, 0, ctx.Err()
		case <-ticker.C:
		}
	}
	f, err := openTorrentFile(path)
	if err != nil {
		return nil, 0, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, err
	}
	if maxBytes > 0 && info.Size() > maxBytes {
		f.Close()
		return nil, 0, fmt.Errorf("download exceeds configured size limit")
	}
	return f, info.Size(), nil
}

func openTorrentFile(path string) (*os.File, error) {
	candidates := []string{path}
	dir, base := filepath.Dir(path), filepath.Base(path)
	// LinuxServer Transmission keeps incomplete payloads in an `incomplete`
	// child directory until completion. Support both layouts without exposing
	// that implementation detail to callers.
	candidates = append(candidates, filepath.Join(dir, "incomplete", base), filepath.Join(dir, "complete", base))
	for _, candidate := range candidates {
		if f, err := os.Open(candidate); err == nil {
			return f, nil
		}
	}
	return nil, os.ErrNotExist
}
