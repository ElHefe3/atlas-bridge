package acquisition

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ElHefe3/atlas-bridge/internal/model"
)

type Manager struct {
	mu        sync.RWMutex
	jobs      map[string]*model.Acquisition
	files     map[string]io.ReadCloser
	providers map[string]model.Provider
	staging   string
	maxBytes  int64
}

func NewManager() *Manager {
	return &Manager{jobs: map[string]*model.Acquisition{}, files: map[string]io.ReadCloser{}}
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
	j := &model.Acquisition{ID: id, ProviderID: req.ProviderID, ExternalID: req.ExternalID, FileID: req.FileID, Status: model.AcquisitionQueued}
	m.mu.Lock()
	m.jobs[id] = j
	m.mu.Unlock()
	if m.providers != nil {
		go m.run(id, req)
	}
	return *j, nil
}

func (m *Manager) run(id string, req model.AcquisitionRequest) {
	m.update(id, func(j *model.Acquisition) { j.Status = model.AcquisitionResolving; j.Progress = .02 })
	p := m.providers[req.ProviderID]
	if p == nil {
		m.fail(id, "provider is not available")
		return
	}
	m.update(id, func(j *model.Acquisition) { j.Status = model.AcquisitionDownloading; j.Progress = .05 })
	remote, err := p.OpenFile(context.Background(), req.ExternalID, req.FileID)
	if err != nil {
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
	if f, err := os.Open(part); err == nil {
		m.files[id] = f
	}
	m.mu.Unlock()
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
	m.update(id, func(j *model.Acquisition) { j.Status = model.AcquisitionFailed; j.Error = msg })
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
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return false
	}
	j.Status = model.AcquisitionCancelled
	return true
}
func (m *Manager) File(id string) (io.ReadCloser, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	f, ok := m.files[id]
	return f, ok
}
