package catalog

import (
	"bufio"
	"context"
	"encoding/json"
	"github.com/ElHefe3/atlas-bridge/internal/model"
	"io"
	"strings"
)

type AnnaRecord struct {
	ProviderID  string `json:"providerId"`
	ExternalID  string `json:"externalId"`
	Title       string `json:"title"`
	Author      string `json:"author"`
	Description string `json:"description"`
	ISBN        string `json:"isbn"`
	Language    string `json:"language"`
	Format      string `json:"format"`
	Size        *int64 `json:"size"`
	URL         string `json:"url"`
	Torrent     string `json:"torrent"`
	Path        string `json:"path"`
}

// SyncAnnaJSONL imports normalized Anna records without loading the dump into memory.
// The input may be produced by a torrent client or a lawful fixture extractor.
func (s *Store) SyncAnnaJSONL(ctx context.Context, input io.Reader, limit int) (int, int, error) {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64*1024), 8<<20)
	records, skipped := 0, 0
	for scanner.Scan() {
		if limit > 0 && records >= limit {
			break
		}
		var r AnnaRecord
		if err := json.Unmarshal(scanner.Bytes(), &r); err != nil {
			skipped++
			continue
		}
		if strings.TrimSpace(r.Title) == "" || strings.TrimSpace(r.ExternalID) == "" {
			skipped++
			continue
		}
		provider := r.ProviderID
		if provider == "" {
			provider = "anna-local"
		}
		format := strings.ToLower(strings.TrimPrefix(r.Format, "."))
		book := model.Book{ProviderID: provider, ExternalID: r.ExternalID, Title: r.Title, Author: r.Author, Description: r.Description, ISBN: r.ISBN, Files: []model.File{{FileID: format, Format: format, Size: r.Size}}}
		var locator *Locator
		if r.URL != "" {
			locator = &Locator{ProviderID: provider, ExternalID: r.ExternalID, FileID: format, Kind: LocatorHTTP, URL: r.URL}
		} else if r.Torrent != "" && r.Path != "" {
			locator = &Locator{ProviderID: provider, ExternalID: r.ExternalID, FileID: format, Kind: LocatorTorrentFile, Metainfo: r.Torrent, Path: r.Path}
		}
		if err := s.ImportRecord(ctx, book, locator); err != nil {
			return records, skipped, err
		}
		records++
	}
	return records, skipped, scanner.Err()
}
