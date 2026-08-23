package acquisition

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ElHefe3/atlas-bridge/internal/catalog"
	"github.com/ElHefe3/atlas-bridge/internal/model"
	"github.com/ElHefe3/atlas-bridge/internal/torrent"
)

type Manager struct {
	mu           sync.RWMutex
	jobs         map[string]*model.Acquisition
	files        map[string]string
	cancels      map[string]context.CancelFunc
	workers      chan struct{}
	providers    map[string]model.Provider
	staging      string
	maxBytes     int64
	locators     *catalog.Store
	transmission *torrent.Transmission
}

func (m *Manager) SetTorrentSources(locators *catalog.Store, client *torrent.Transmission) {
	m.locators = locators
	m.transmission = client
}

func NewManager() *Manager {
	return &Manager{
		jobs:    map[string]*model.Acquisition{},
		files:   map[string]string{},
		cancels: map[string]context.CancelFunc{},
		// A single worker is the safe default because Anna data torrents are
		// shared across many books. Transmission de-duplicates torrents by
		// infohash, so concurrent file-selection changes would otherwise race.
		workers: make(chan struct{}, 1),
	}
}

func NewManagerWithProviders(providers []model.Provider, staging string, maxBytes int64) *Manager {
	m := NewManager()
	m.staging = staging
	m.maxBytes = maxBytes
	m.providers = make(map[string]model.Provider, len(providers))
	for _, p := range providers {
		m.providers[p.Info().ID] = p
	}
	return m
}

func (m *Manager) Create(_ context.Context, req model.AcquisitionRequest) (model.Acquisition, error) {
	idb := make([]byte, 12)
	if _, err := rand.Read(idb); err != nil {
		return model.Acquisition{}, err
	}
	id := hex.EncodeToString(idb)
	j := &model.Acquisition{ID: id, ProviderID: req.ProviderID, ExternalID: req.ExternalID, FileID: req.FileID, Format: strings.ToLower(strings.TrimSpace(req.FileID)), Status: model.AcquisitionQueued}
	m.mu.Lock()
	m.jobs[id] = j
	m.mu.Unlock()
	if m.providers != nil {
		workerCtx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		m.mu.Lock()
		m.cancels[id] = cancel
		m.mu.Unlock()
		go func() {
			defer func() {
				cancel()
				m.mu.Lock()
				delete(m.cancels, id)
				m.mu.Unlock()
			}()
			select {
			case m.workers <- struct{}{}:
				defer func() { <-m.workers }()
			case <-workerCtx.Done():
				m.fail(id, "acquisition timed out while queued")
				return
			}
			m.run(workerCtx, id, req)
		}()
	}
	return *j, nil
}

func (m *Manager) run(ctx context.Context, id string, req model.AcquisitionRequest) {
	m.update(id, func(j *model.Acquisition) { j.Status = model.AcquisitionResolving; j.Progress = .02 })
	p := m.providers[req.ProviderID]
	if p == nil {
		m.fail(id, "provider is not available")
		return
	}
	m.update(id, func(j *model.Acquisition) { j.Status = model.AcquisitionDownloading; j.Progress = .05 })
	var locator catalog.Locator
	hasTorrentLocator := false
	if m.locators != nil && m.transmission != nil {
		if indexed, ok, err := m.locators.GetLocator(ctx, req.ProviderID, req.ExternalID, req.FileID); err == nil && ok && indexed.Kind == catalog.LocatorTorrentFile {
			locator, hasTorrentLocator = indexed, true
		}
	}
	// The local catalogue's deterministic selected-file torrent is the primary
	// acquisition path. Keep the provider's bounded member resolver as a
	// fallback when a torrent has no peers or cannot be loaded.
	var torrentErr error
	if hasTorrentLocator {
		torrentCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		torrentErr = m.runTorrent(torrentCtx, id, req, locator)
		cancel()
		if torrentErr == nil {
			return
		}
	}
	remote, err := p.OpenFile(ctx, req.ExternalID, req.FileID)
	if err != nil {
		if torrentErr != nil {
			err = fmt.Errorf("torrent acquisition failed: %v; direct fallback failed: %w", torrentErr, err)
		}
		m.fail(id, err.Error())
		return
	}
	defer remote.Body.Close()
	if remote.Size != nil {
		m.update(id, func(j *model.Acquisition) { j.TotalBytes = remote.Size })
	}
	if err := os.MkdirAll(m.staging, 0o700); err != nil {
		m.fail(id, err.Error())
		return
	}
	part := filepath.Join(m.staging, id+".part")
	out, err := os.OpenFile(part, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		m.fail(id, err.Error())
		return
	}
	var copied int64
	buf := make([]byte, 64*1024)
	for {
		n, readErr := remote.Body.Read(buf)
		if n > 0 {
			copied += int64(n)
			if m.maxBytes > 0 && copied > m.maxBytes {
				out.Close()
				_ = os.Remove(part)
				m.fail(id, "download exceeds configured limit")
				return
			}
			if _, err = out.Write(buf[:n]); err != nil {
				out.Close()
				_ = os.Remove(part)
				m.fail(id, err.Error())
				return
			}
			m.update(id, func(j *model.Acquisition) {
				j.Bytes = copied
				if j.TotalBytes != nil && *j.TotalBytes > 0 {
					j.Progress = .05 + .9*float64(copied)/float64(*j.TotalBytes)
				}
			})
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			out.Close()
			_ = os.Remove(part)
			m.fail(id, readErr.Error())
			return
		}
	}
	if err = out.Sync(); err != nil {
		out.Close()
		_ = os.Remove(part)
		m.fail(id, err.Error())
		return
	}
	if err = out.Close(); err != nil {
		_ = os.Remove(part)
		m.fail(id, err.Error())
		return
	}
	if strings.EqualFold(req.FileID, "epub") || strings.EqualFold(req.FileID, "pdf") {
		if !validSignature(part, req.FileID) {
			_ = os.Remove(part)
			m.fail(id, "downloaded file failed signature validation")
			return
		}
	}
	m.mu.Lock()
	if j := m.jobs[id]; j != nil {
		j.Status = model.AcquisitionCompleted
		j.Progress = 1
		j.Bytes = copied
	}
	m.mu.Unlock()
	m.mu.Lock()
	m.files[id] = part
	m.mu.Unlock()
}

func (m *Manager) runTorrent(ctx context.Context, id string, req model.AcquisitionRequest, locator catalog.Locator) error {
	if filepath.IsAbs(locator.Path) || strings.Contains(filepath.ToSlash(locator.Path), "../") {
		return fmt.Errorf("unsafe torrent path")
	}
	sharedRoot := filepath.Join(filepath.Dir(m.staging), "torrents")
	if err := os.MkdirAll(m.staging, 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(sharedRoot, 0o700); err != nil {
		return err
	}
	dir := filepath.Join(sharedRoot, id+"-torrent")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	file, size, err := m.transmission.DownloadFile(ctx, torrent.AddRequest{Metainfo: locator.Metainfo, DownloadDir: filepath.Join("/downloads", id+"-torrent")}, filepath.Join(dir, locator.Path), m.maxBytes, func(status torrent.Status) {
		m.update(id, func(j *model.Acquisition) {
			j.Bytes = status.DownloadedEver
			if status.SizeWhenDone > 0 {
				total := status.SizeWhenDone
				j.TotalBytes = &total
				j.Progress = .05 + .9*status.PercentDone
			}
		})
	})
	if err != nil {
		return err
	}
	defer file.Close()
	part := filepath.Join(m.staging, id+".part")
	out, err := os.OpenFile(part, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, file)
	syncErr := out.Sync()
	closeErr := out.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil {
		_ = os.Remove(part)
		return fmt.Errorf("torrent staging failed")
	}
	if !validSignature(part, req.FileID) {
		_ = os.Remove(part)
		return fmt.Errorf("downloaded file failed signature validation")
	}
	m.mu.Lock()
	if j := m.jobs[id]; j != nil {
		j.Status = model.AcquisitionCompleted
		j.Progress = 1
		j.Bytes = size
	}
	m.files[id] = part
	m.mu.Unlock()
	return nil
}

func validSignature(path, format string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	b := make([]byte, 8)
	n, _ := f.Read(b)
	if format == "pdf" {
		return n >= 5 && string(b[:5]) == "%PDF-"
	}
	return n >= 4 && string(b[:2]) == "PK"
}
func (m *Manager) update(id string, fn func(*model.Acquisition)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if j := m.jobs[id]; j != nil {
		fn(j)
	}
}
func (m *Manager) fail(id, msg string) {
	m.update(id, func(j *model.Acquisition) {
		if j.Status != model.AcquisitionCancelled {
			j.Status = model.AcquisitionFailed
			j.Error = msg
		}
	})
}
func (m *Manager) Get(id string) (model.Acquisition, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	j, ok := m.jobs[id]
	if !ok {
		return model.Acquisition{}, false
	}
	return *j, true
}
func (m *Manager) Cancel(id string) bool {
	m.mu.Lock()
	j, ok := m.jobs[id]
	if !ok {
		m.mu.Unlock()
		return false
	}
	j.Status = model.AcquisitionCancelled
	cancel := m.cancels[id]
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return true
}
func (m *Manager) File(id string) (io.ReadCloser, bool) {
	m.mu.Lock()
	path, ok := m.files[id]
	if ok {
		delete(m.files, id)
	}
	m.mu.Unlock()
	if !ok {
		return nil, false
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	return &cleanupFile{File: f, path: path}, true
}

type cleanupFile struct {
	*os.File
	path string
}

func (f *cleanupFile) Close() error {
	err := f.File.Close()
	if removeErr := os.Remove(f.path); err == nil && removeErr != nil && !os.IsNotExist(removeErr) {
		err = removeErr
	}
	return err
}
