package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/ElHefe3/atlas-bridge/internal/model"
)

type LocatorKind string

const (
	LocatorHTTP         LocatorKind = "http"
	LocatorTorrentFile  LocatorKind = "torrent-file"
	LocatorTorrentRange LocatorKind = "torrent-range"
)

type Locator struct {
	ProviderID string      `json:"providerId"`
	ExternalID string      `json:"externalId"`
	FileID     string      `json:"fileId"`
	Kind       LocatorKind `json:"kind"`
	URL        string      `json:"url,omitempty"`
	Metainfo   string      `json:"metainfo,omitempty"`
	Path       string      `json:"path,omitempty"`
	Offset     int64       `json:"offset,omitempty"`
	Length     int64       `json:"length,omitempty"`
}

func (s *Store) PutLocator(ctx context.Context, l Locator) error {
	data, err := json.Marshal(l)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS locators(provider_id TEXT, external_id TEXT, file_id TEXT, data TEXT NOT NULL, PRIMARY KEY(provider_id,external_id,file_id)); INSERT INTO locators(provider_id,external_id,file_id,data) VALUES(?,?,?,?) ON CONFLICT(provider_id,external_id,file_id) DO UPDATE SET data=excluded.data`, l.ProviderID, l.ExternalID, l.FileID, string(data))
	return err
}
func (s *Store) GetLocator(ctx context.Context, provider, externalID, fileID string) (Locator, bool, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT data FROM locators WHERE provider_id=? AND external_id=? AND file_id=?`, provider, externalID, fileID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return Locator{}, false, nil
	}
	if err != nil {
		return Locator{}, false, err
	}
	var l Locator
	if err = json.Unmarshal([]byte(raw), &l); err != nil {
		return Locator{}, false, err
	}
	return l, true, nil
}

func (s *Store) ImportRecord(ctx context.Context, book model.Book, locator *Locator) error {
	if err := s.Upsert(ctx, book); err != nil {
		return err
	}
	if locator != nil {
		return s.PutLocator(ctx, *locator)
	}
	return nil
}
