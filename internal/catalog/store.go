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

	_ "modernc.org/sqlite"

	"github.com/ElHefe3/atlas-bridge/internal/model"
)

// Store is a small embedded catalogue. It intentionally stores only normalized
// metadata; source credentials and signed URLs are never persisted here.
type Store struct{ db *sql.DB }
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
	if _, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA synchronous=NORMAL;
CREATE TABLE IF NOT EXISTS books (provider_id TEXT NOT NULL, external_id TEXT NOT NULL, title TEXT NOT NULL, author TEXT, description TEXT, isbn TEXT, cover_url TEXT, files_json TEXT NOT NULL, PRIMARY KEY(provider_id, external_id));
CREATE TABLE IF NOT EXISTS file_records (provider_id TEXT NOT NULL, external_id TEXT NOT NULL, file_id TEXT NOT NULL, md5 TEXT, aacid TEXT, format TEXT NOT NULL, size INTEGER, PRIMARY KEY(provider_id, external_id, file_id));
CREATE VIRTUAL TABLE IF NOT EXISTS books_fts USING fts5(provider_id UNINDEXED, external_id UNINDEXED, title, author, isbn, content='books', content_rowid='rowid');
CREATE TABLE IF NOT EXISTS locators (provider_id TEXT NOT NULL, external_id TEXT NOT NULL, file_id TEXT NOT NULL, data TEXT NOT NULL, PRIMARY KEY(provider_id, external_id, file_id));`); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Upsert(ctx context.Context, book model.Book) error {
	return s.upsertExec(ctx, s.db, book)
}

func (s *Store) UpsertBatch(ctx context.Context, books []model.Book) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	for _, book := range books {
		if err := s.upsertExec(ctx, tx, book); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) UpsertFilesBatch(ctx context.Context, files []FileRecord) error {
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
	return tx.Commit()
}

type FileRecord struct {
	ProviderID, ExternalID, FileID, MD5, AACID, Format string
	Size                                               *int64
	Locator                                            *Locator
}

func (s *Store) upsertExec(ctx context.Context, exec sqlExecer, book model.Book) error {
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
	_, _ = exec.ExecContext(ctx, `DELETE FROM books_fts WHERE rowid=?`, rowid)
	_, err = exec.ExecContext(ctx, `INSERT INTO books_fts(rowid,provider_id,external_id,title,author,isbn) VALUES(?,?,?,?,?,?)`, rowid, book.ProviderID, book.ExternalID, book.Title, book.Author, book.ISBN)
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
	terms := strings.Fields(query)
	quoted := make([]string, 0, len(terms))
	for _, term := range terms {
		term = strings.ReplaceAll(term, `"`, "")
		if term != "" {
			quoted = append(quoted, `"`+term+`"*`)
		}
	}
	query = strings.Join(quoted, " AND ")
	args := []any{query, pageSize, (page - 1) * pageSize}
	filter := ""
	if format != "" {
		filter = " AND EXISTS (SELECT 1 FROM json_each(books.files_json) f WHERE lower(json_extract(f.value,'$.format')) = ?)"
		args = []any{query, format, pageSize, (page - 1) * pageSize}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT provider_id,external_id,title,author,description,isbn,cover_url,files_json FROM books WHERE rowid IN (SELECT rowid FROM books_fts WHERE books_fts MATCH ?) `+filter+` ORDER BY title LIMIT ? OFFSET ?`, args...)
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
	_, err := s.db.ExecContext(ctx, `INSERT INTO books_fts(rowid,provider_id,external_id,title,author,isbn) SELECT rowid,provider_id,external_id,title,author,isbn FROM books`)
	return err
}

func (s *Store) String() string { return fmt.Sprintf("catalogue(%p)", s) }
