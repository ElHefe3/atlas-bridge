package catalog

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/ElHefe3/atlas-bridge/internal/model"
)

// Store is a small embedded catalogue. It intentionally stores only normalized
// metadata; source credentials and signed URLs are never persisted here.
type Store struct {
	db      *sql.DB
	writeMu sync.Mutex
}
type sqlExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	if _, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA synchronous=NORMAL;
CREATE TABLE IF NOT EXISTS books (provider_id TEXT NOT NULL, external_id TEXT NOT NULL, title TEXT NOT NULL, author TEXT, description TEXT, isbn TEXT, cover_url TEXT, files_json TEXT NOT NULL, PRIMARY KEY(provider_id, external_id));
CREATE TABLE IF NOT EXISTS file_records (provider_id TEXT NOT NULL, external_id TEXT NOT NULL, file_id TEXT NOT NULL, md5 TEXT, aacid TEXT, format TEXT NOT NULL, size INTEGER, PRIMARY KEY(provider_id, external_id, file_id));
CREATE TABLE IF NOT EXISTS source_file_map (source_id TEXT PRIMARY KEY, md5 TEXT NOT NULL, aacid TEXT, data_folder TEXT);
CREATE VIRTUAL TABLE IF NOT EXISTS books_fts USING fts5(provider_id UNINDEXED, external_id UNINDEXED, title, author, isbn, content='books', content_rowid='rowid');
CREATE TABLE IF NOT EXISTS locators (provider_id TEXT NOT NULL, external_id TEXT NOT NULL, file_id TEXT NOT NULL, data TEXT NOT NULL, PRIMARY KEY(provider_id, external_id, file_id));`); err != nil {
		db.Close()
		return nil, err
	}
	// This is a first line of defence for readers and external maintenance
	// tools. All bridge writes are also serialized and retried below.
	_, _ = db.Exec(`PRAGMA busy_timeout=30000`)
	return &Store{db: db}, nil
}

func (s *Store) Upsert(ctx context.Context, book model.Book) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.withWriteRetry(ctx, func() error { return s.upsertExec(ctx, s.db, book, true) })
}

func (s *Store) UpsertBatch(ctx context.Context, books []model.Book) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.withWriteRetry(ctx, func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		for _, book := range books {
			if err := s.upsertExec(ctx, tx, book, false); err != nil {
				_ = tx.Rollback()
				return err
			}
		}
		if err := tx.Commit(); err != nil {
			_ = tx.Rollback()
			return err
		}
		return nil
	})
}

func (s *Store) UpsertFilesBatch(ctx context.Context, files []FileRecord) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.withWriteRetry(ctx, func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		for _, f := range files {
			if _, err := tx.ExecContext(ctx, `INSERT INTO file_records(provider_id,external_id,file_id,md5,aacid,format,size) VALUES(?,?,?,?,?,?,?) ON CONFLICT(provider_id,external_id,file_id) DO UPDATE SET md5=excluded.md5,aacid=excluded.aacid,format=excluded.format,size=excluded.size`, f.ProviderID, f.ExternalID, f.FileID, nullable(f.MD5), nullable(f.AACID), f.Format, f.Size); err != nil {
				_ = tx.Rollback()
				return err
			}
			if f.Locator != nil {
				data, err := json.Marshal(*f.Locator)
				if err != nil {
					_ = tx.Rollback()
					return err
				}
				if _, err := tx.ExecContext(ctx, `INSERT INTO locators(provider_id,external_id,file_id,data) VALUES(?,?,?,?) ON CONFLICT(provider_id,external_id,file_id) DO UPDATE SET data=excluded.data`, f.Locator.ProviderID, f.Locator.ExternalID, f.Locator.FileID, string(data)); err != nil {
					_ = tx.Rollback()
					return err
				}
			}
		}
		if err := tx.Commit(); err != nil {
			_ = tx.Rollback()
			return err
		}
		return nil
	})
}

// withWriteRetry makes ingestion resilient to short-lived WAL/checkpoint
// contention from API writes and maintenance operations. The mutex prevents
// bridge goroutines from competing with each other; retries cover readers or
// external sqlite tools that may still hold a lock.
func (s *Store) withWriteRetry(ctx context.Context, fn func() error) error {
	backoff := 100 * time.Millisecond
	for attempt := 0; ; attempt++ {
		err := fn()
		if err == nil || !isSQLiteBusy(err) || attempt >= 9 {
			return err
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		if backoff < 2*time.Second {
			backoff *= 2
		}
	}
}

func isSQLiteBusy(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "database is locked") || strings.Contains(message, "sqlite_busy") || strings.Contains(message, "database table is locked")
}

type FileRecord struct {
	ProviderID, ExternalID, FileID, MD5, AACID, Format string
	Size                                               *int64
	Locator                                            *Locator
}

type SourceFileMap struct {
	SourceID, MD5, AACID, DataFolder string
}

func (s *Store) UpsertSourceFileMapsBatch(ctx context.Context, mappings []SourceFileMap) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	for _, mapping := range mappings {
		if _, err = tx.ExecContext(ctx, `INSERT INTO source_file_map(source_id,md5,aacid,data_folder) VALUES(?,?,?,?) ON CONFLICT(source_id) DO UPDATE SET md5=excluded.md5,aacid=excluded.aacid,data_folder=excluded.data_folder`, mapping.SourceID, strings.ToLower(mapping.MD5), nullable(mapping.AACID), nullable(mapping.DataFolder)); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) upsertExec(ctx context.Context, exec sqlExecer, book model.Book, index bool) error {
	b, err := json.Marshal(book.Files)
	if err != nil {
		return err
	}
	_, err = exec.ExecContext(ctx, `INSERT INTO books(provider_id,external_id,title,author,description,isbn,cover_url,files_json) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(provider_id,external_id) DO UPDATE SET title=excluded.title,author=excluded.author,description=excluded.description,isbn=excluded.isbn,cover_url=excluded.cover_url,files_json=excluded.files_json`, book.ProviderID, book.ExternalID, book.Title, book.Author, book.Description, book.ISBN, book.CoverURL, string(b))
	if err != nil {
		return err
	}
	var rowid int64
	if err = exec.QueryRowContext(ctx, `SELECT rowid FROM books WHERE provider_id=? AND external_id=?`, book.ProviderID, book.ExternalID).Scan(&rowid); err != nil {
		return err
	}
	if index {
		_, _ = exec.ExecContext(ctx, `DELETE FROM books_fts WHERE rowid=?`, rowid)
		_, err = exec.ExecContext(ctx, `INSERT INTO books_fts(rowid,provider_id,external_id,title,author,isbn) VALUES(?,?,?,?,?,?)`, rowid, book.ProviderID, book.ExternalID, book.Title, book.Author, book.ISBN)
	}
	for _, file := range book.Files {
		if _, fileErr := exec.ExecContext(ctx, `INSERT INTO file_records(provider_id,external_id,file_id,md5,aacid,format,size) VALUES(?,?,?,?,?,?,?) ON CONFLICT(provider_id,external_id,file_id) DO UPDATE SET md5=excluded.md5,aacid=excluded.aacid,format=excluded.format,size=excluded.size`, book.ProviderID, book.ExternalID, file.FileID, nullable(file.MD5), nullable(file.AACID), file.Format, file.Size); fileErr != nil {
			return fileErr
		}
	}
	return err
}

func nullable(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func (s *Store) Search(ctx context.Context, query string, page, pageSize int, format string) ([]model.Book, bool, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, false, nil
	}
	// Treat user input as terms, never as an FTS expression. This prevents
	// punctuation such as ':' or '-' from changing the query grammar.
	query = ftsQuery(query)
	args := []any{query, pageSize, (page - 1) * pageSize}
	filter := ""
	if format != "" {
		filter = " AND EXISTS (SELECT 1 FROM file_records f WHERE f.provider_id=books.provider_id AND f.external_id=books.external_id AND lower(f.format)=?)"
		args = []any{query, strings.ToLower(format), pageSize, (page - 1) * pageSize}
	}
	// ORDER BY rank allows FTS5 to stop once the requested page is found. The
	// previous alphabetical sort materialized every match and became unusably
	// slow on the full zlib3 catalogue.
	rows, err := s.db.QueryContext(ctx, `SELECT books.provider_id,books.external_id,books.title,books.author,books.description,books.isbn,books.cover_url,books.files_json FROM books_fts JOIN books ON books.rowid=books_fts.rowid WHERE books_fts MATCH ? `+filter+` ORDER BY rank LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var out []model.Book
	for rows.Next() {
		var b model.Book
		var files string
		if err := rows.Scan(&b.ProviderID, &b.ExternalID, &b.Title, &b.Author, &b.Description, &b.ISBN, &b.CoverURL, &files); err != nil {
			return nil, false, err
		}
		if err := json.Unmarshal([]byte(files), &b.Files); err != nil {
			return nil, false, err
		}
		if merged, mergeErr := s.fileRecords(ctx, b.ProviderID, b.ExternalID); mergeErr != nil {
			return nil, false, mergeErr
		} else if len(merged) > 0 {
			b.Files = merged
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	return out, len(out) == pageSize, nil
}

var searchStopwords = map[string]struct{}{
	"a": {}, "an": {}, "and": {}, "as": {}, "at": {}, "by": {}, "for": {}, "from": {},
	"in": {}, "of": {}, "on": {}, "or": {}, "the": {}, "to": {}, "with": {},
}

func ftsQuery(input string) string {
	terms := strings.Fields(input)
	filtered := make([]string, 0, len(terms))
	for _, term := range terms {
		term = strings.ReplaceAll(term, `"`, "")
		if term == "" {
			continue
		}
		if len(terms) > 1 {
			if _, stopword := searchStopwords[strings.ToLower(term)]; stopword {
				continue
			}
		}
		filtered = append(filtered, `"`+term+`"*`)
	}
	if len(filtered) == 0 {
		phrase := strings.ReplaceAll(strings.TrimSpace(input), `"`, "")
		return `"` + phrase + `"`
	}
	return strings.Join(filtered, " AND ")
}

func (s *Store) GetBook(ctx context.Context, provider, external string) (model.Book, bool, error) {
	var b model.Book
	var files string
	err := s.db.QueryRowContext(ctx, `SELECT provider_id,external_id,title,author,description,isbn,cover_url,files_json FROM books WHERE provider_id=? AND external_id=?`, provider, external).Scan(&b.ProviderID, &b.ExternalID, &b.Title, &b.Author, &b.Description, &b.ISBN, &b.CoverURL, &files)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Book{}, false, nil
	}
	if err != nil {
		return model.Book{}, false, err
	}
	if err := json.Unmarshal([]byte(files), &b.Files); err != nil {
		return model.Book{}, false, err
	}
	merged, err := s.fileRecords(ctx, provider, external)
	if err != nil {
		return model.Book{}, false, err
	}
	if len(merged) > 0 {
		b.Files = merged
	}
	return b, true, nil
}

func (s *Store) fileRecords(ctx context.Context, provider, external string) ([]model.File, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT file_id,format,size,md5,aacid FROM file_records WHERE provider_id=? AND external_id=? ORDER BY format`, provider, external)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var files []model.File
	for rows.Next() {
		var f model.File
		var md5, aacid sql.NullString
		if err := rows.Scan(&f.FileID, &f.Format, &f.Size, &md5, &aacid); err != nil {
			return nil, err
		}
		f.MD5 = md5.String
		f.AACID = aacid.String
		files = append(files, f)
	}
	return files, rows.Err()
}

// GetFile returns the normalized file metadata used by acquisition resolvers.
// Retrieval locators stay internal to the bridge and are never serialized by
// the public API.
func (s *Store) GetFile(ctx context.Context, provider, external, fileID string) (model.File, bool, error) {
	var f model.File
	var md5, aacid sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT file_id,format,size,md5,aacid FROM file_records WHERE provider_id=? AND external_id=? AND file_id=?`, provider, external, fileID).Scan(&f.FileID, &f.Format, &f.Size, &md5, &aacid)
	if errors.Is(err, sql.ErrNoRows) {
		return model.File{}, false, nil
	}
	if err != nil {
		return model.File{}, false, err
	}
	f.MD5, f.AACID = md5.String, aacid.String
	if f.MD5 == "" {
		if sourceID := sourceIDFromAACID(external); sourceID != "" {
			var mappedMD5, mappedAACID sql.NullString
			if mapErr := s.db.QueryRowContext(ctx, `SELECT md5,aacid FROM source_file_map WHERE source_id=?`, sourceID).Scan(&mappedMD5, &mappedAACID); mapErr == nil {
				f.MD5, f.AACID = mappedMD5.String, mappedAACID.String
			} else if !errors.Is(mapErr, sql.ErrNoRows) {
				return model.File{}, false, mapErr
			}
		}
	}
	return f, true, nil
}

func (s *Store) SetCoverURL(ctx context.Context, provider, external, coverURL string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.withWriteRetry(ctx, func() error {
		_, err := s.db.ExecContext(ctx, `UPDATE books SET cover_url=? WHERE provider_id=? AND external_id=?`, coverURL, provider, external)
		return err
	})
}

func sourceIDFromAACID(value string) string {
	parts := strings.Split(value, "__")
	if len(parts) < 5 {
		return ""
	}
	return strings.TrimSpace(parts[len(parts)-2])
}

func (s *Store) Close() error { return s.db.Close() }

// IngestJSONL streams normalized records so a multi-gigabyte dataset never
// needs to be loaded into memory. Unknown fields are ignored deliberately.
func (s *Store) IngestJSONL(ctx context.Context, input io.Reader, limit int) (int, error) {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64*1024), 8<<20)
	count := 0
	for scanner.Scan() {
		if limit > 0 && count >= limit {
			break
		}
		var b model.Book
		if err := json.Unmarshal(scanner.Bytes(), &b); err != nil {
			return count, err
		}
		if strings.TrimSpace(b.ProviderID) == "" || strings.TrimSpace(b.ExternalID) == "" || strings.TrimSpace(b.Title) == "" {
			continue
		}
		if err := s.Upsert(ctx, b); err != nil {
			return count, err
		}
		count++
	}
	return count, scanner.Err()
}

func (s *Store) RebuildFTS(ctx context.Context) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.withWriteRetry(ctx, func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM books_fts`); err != nil {
			_ = tx.Rollback()
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO books_fts(rowid,provider_id,external_id,title,author,isbn) SELECT rowid,provider_id,external_id,title,author,isbn FROM books`); err != nil {
			_ = tx.Rollback()
			return err
		}
		return tx.Commit()
	})
}

func (s *Store) String() string { return fmt.Sprintf("catalogue(%p)", s) }
