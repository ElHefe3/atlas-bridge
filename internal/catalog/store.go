package catalog

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
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
CREATE VIRTUAL TABLE IF NOT EXISTS books_fts USING fts5(provider_id UNINDEXED, external_id UNINDEXED, title, author, isbn, content='books', content_rowid='rowid');
CREATE TABLE IF NOT EXISTS locators (provider_id TEXT NOT NULL, external_id TEXT NOT NULL, file_id TEXT NOT NULL, data TEXT NOT NULL, PRIMARY KEY(provider_id, external_id, file_id));`); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Upsert(ctx context.Context, book model.Book) error {
	b, err := json.Marshal(book.Files)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO books(provider_id,external_id,title,author,description,isbn,cover_url,files_json) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(provider_id,external_id) DO UPDATE SET title=excluded.title,author=excluded.author,description=excluded.description,isbn=excluded.isbn,cover_url=excluded.cover_url,files_json=excluded.files_json`, book.ProviderID, book.ExternalID, book.Title, book.Author, book.Description, book.ISBN, book.CoverURL, string(b))
	if err != nil {
		return err
	}
	var rowid int64
	if err = s.db.QueryRowContext(ctx, `SELECT rowid FROM books WHERE provider_id=? AND external_id=?`, book.ProviderID, book.ExternalID).Scan(&rowid); err != nil {
		return err
	}
	_, _ = s.db.ExecContext(ctx, `DELETE FROM books_fts WHERE rowid=?`, rowid)
	_, err = s.db.ExecContext(ctx, `INSERT INTO books_fts(rowid,provider_id,external_id,title,author,isbn) VALUES(?,?,?,?,?,?)`, rowid, book.ProviderID, book.ExternalID, book.Title, book.Author, book.ISBN)
	return err
}

func (s *Store) Search(ctx context.Context, query string, page, pageSize int, format string) ([]model.Book, bool, error) {
	query = strings.TrimSpace(query)
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
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	return out, len(out) == pageSize, nil
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
