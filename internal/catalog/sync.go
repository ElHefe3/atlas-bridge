package catalog

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"github.com/ElHefe3/atlas-bridge/internal/model"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/klauspost/compress/zstd"
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
	CoverURL    string `json:"coverUrl"`
	MD5         string `json:"md5"`
	AACID       string `json:"aacid"`
	FileSize    *int64 `json:"filesize"`
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
		r, err := decodeAnnaRecord(scanner.Bytes())
		if err != nil {
			skipped++
			continue
		}
		if strings.TrimSpace(r.Title) == "" {
			skipped++
			continue
		}
		if r.ExternalID == "" {
			r.ExternalID = r.MD5
			if r.ExternalID == "" {
				r.ExternalID = r.AACID
			}
		}
		if strings.TrimSpace(r.ExternalID) == "" {
			skipped++
			continue
		}
		provider := r.ProviderID
		if provider == "" {
			provider = "anna-local"
		}
		format := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(r.Format), "."))
		if format == "" {
			format = "unknown"
		}
		size := r.Size
		if size == nil {
			size = r.FileSize
		}
		book := model.Book{ProviderID: provider, ExternalID: r.ExternalID, Title: r.Title, Author: r.Author, Description: r.Description, ISBN: r.ISBN, CoverURL: r.CoverURL, Files: []model.File{{FileID: format, Format: format, Size: size}}}
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

// decodeAnnaRecord accepts both the bridge's normalized shape and common Anna
// metadata aliases. It intentionally ignores unknown/nested source fields.
func decodeAnnaRecord(data []byte) (AnnaRecord, error) {
	var r AnnaRecord
	if err := json.Unmarshal(data, &r); err != nil {
		return r, err
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return r, err
	}
	str := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := raw[k].(string); ok && strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		}
		return ""
	}
	if r.Title == "" {
		r.Title = str("title", "title_sort", "book_title")
	}
	if r.Author == "" {
		r.Author = str("author", "authors", "author_name")
	}
	if r.ISBN == "" {
		r.ISBN = str("isbn", "isbn13", "isbn_10")
	}
	if r.ExternalID == "" {
		r.ExternalID = str("externalId", "external_id", "md5", "aacid")
	}
	if r.MD5 == "" {
		r.MD5 = str("md5")
	}
	if r.AACID == "" {
		r.AACID = str("aacid")
	}
	if r.Format == "" {
		r.Format = str("format", "extension", "ext")
	}
	if r.CoverURL == "" {
		r.CoverURL = str("coverUrl", "cover_url", "cover")
	}
	if r.Torrent == "" {
		r.Torrent = str("torrent", "torrent_url", "metainfo")
	}
	if r.Path == "" {
		r.Path = str("path", "torrent_path", "file_path")
	}
	if r.Size == nil {
		r.Size = parseInt64(raw["size"])
	}
	if r.Size == nil {
		r.Size = parseInt64(raw["filesize"])
	}
	if r.FileSize == nil {
		r.FileSize = parseInt64(raw["filesize"])
	}
	return r, nil
}

// IngestZstdJSONL streams a seekable or regular zstd-compressed JSONL dump.
// It is deliberately bounded so a bad community dataset cannot exhaust the bridge.
func (s *Store) IngestZstdJSONL(ctx context.Context, path string, limit int, maxExpanded int64) (int, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	dec, err := zstd.NewReader(f, zstd.WithDecoderConcurrency(1))
	if err != nil {
		return 0, 0, fmt.Errorf("open zstd dataset: %w", err)
	}
	defer dec.Close()
	var input io.Reader = dec
	if maxExpanded > 0 {
		input = &countingReader{r: dec, max: maxExpanded}
	}
	records, skipped, err := s.SyncAnnaJSONL(ctx, input, limit)
	if err != nil {
		return records, skipped, err
	}
	if cr, ok := input.(*countingReader); ok && cr.err != nil {
		return records, skipped, cr.err
	}
	return records, skipped, nil
}

type countingReader struct {
	r      io.Reader
	n, max int64
	err    error
}

func (r *countingReader) Read(p []byte) (int, error) {
	if r.n >= r.max {
		r.err = fmt.Errorf("expanded metadata exceeds limit of %d bytes", r.max)
		return 0, r.err
	}
	if int64(len(p)) > r.max-r.n {
		p = p[:r.max-r.n]
	}
	n, err := r.r.Read(p)
	r.n += int64(n)
	if err != nil {
		r.err = err
		if err == io.EOF {
			r.err = nil
		}
	}
	return n, err
}

func parseInt64(value any) *int64 {
	switch v := value.(type) {
	case float64:
		n := int64(v)
		return &n
	case string:
		n, e := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if e == nil {
			return &n
		}
	}
	return nil
}
