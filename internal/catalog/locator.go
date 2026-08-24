package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/ElHefe3/atlas-bridge/internal/model"
	"net/url"
	"regexp"
	"strings"
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
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.withWriteRetry(ctx, func() error {
		_, err = s.db.ExecContext(ctx, `INSERT INTO locators(provider_id,external_id,file_id,data) VALUES(?,?,?,?) ON CONFLICT(provider_id,external_id,file_id) DO UPDATE SET data=excluded.data`, l.ProviderID, l.ExternalID, l.FileID, string(data))
		return err
	})
}
func (s *Store) GetLocator(ctx context.Context, provider, externalID, fileID string) (Locator, bool, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT data FROM locators WHERE provider_id=? AND external_id=? AND file_id=?`, provider, externalID, fileID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return s.annaSourceLocator(ctx, provider, externalID, fileID)
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

var (
	annaDataFolder = regexp.MustCompile(`^annas_archive_data__aacid__zlib3_files__[0-9TZ-]+$`)
	annaFileAACID  = regexp.MustCompile(`^aacid__zlib3_files__[A-Za-z0-9_-]+$`)
)

// annaSourceLocator derives a private selected-file locator from the official
// zlib3 availability map. The public API never serializes this value.
func (s *Store) annaSourceLocator(ctx context.Context, provider, externalID, fileID string) (Locator, bool, error) {
	if provider != "anna-local" {
		return Locator{}, false, nil
	}
	sourceID := sourceIDFromAACID(externalID)
	if sourceID == "" {
		return Locator{}, false, nil
	}
	var aacid, folder string
	err := s.db.QueryRowContext(ctx, `SELECT aacid,data_folder FROM source_file_map WHERE source_id=?`, sourceID).Scan(&aacid, &folder)
	if errors.Is(err, sql.ErrNoRows) {
		return Locator{}, false, nil
	}
	if err != nil {
		return Locator{}, false, err
	}
	aacid, folder = strings.TrimSpace(aacid), strings.TrimSpace(folder)
	if !annaDataFolder.MatchString(folder) || !annaFileAACID.MatchString(aacid) {
		return Locator{}, false, nil
	}
	metainfo := "https://annas-archive.pk/dyn/small_file/torrents/managed_by_aa/annas_archive_data__aacid/" + url.PathEscape(folder) + ".torrent"
	return Locator{ProviderID: provider, ExternalID: externalID, FileID: fileID, Kind: LocatorTorrentFile, Metainfo: metainfo, Path: folder + "/" + aacid}, true, nil
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
