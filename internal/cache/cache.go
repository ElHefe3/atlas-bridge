package cache

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/ElHefe3/atlas-bridge/internal/model"
	bolt "go.etcd.io/bbolt"
)

var booksBucket = []byte("books-v1")

type entry struct {
	ExpiresAt time.Time  `json:"expiresAt"`
	Book      model.Book `json:"book"`
}

type Store struct {
	db  *bolt.DB
	ttl time.Duration
}

func Open(path string, ttl time.Duration) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 2 * time.Second})
	if err != nil {
		return nil, err
	}
	if err = db.Update(func(tx *bolt.Tx) error { _, e := tx.CreateBucketIfNotExists(booksBucket); return e }); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db, ttl: ttl}, nil
}

func key(provider, id string) []byte { return []byte(provider + ":" + id) }

func (s *Store) Put(book model.Book) error {
	b, err := json.Marshal(entry{ExpiresAt: time.Now().Add(s.ttl), Book: book})
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error { return tx.Bucket(booksBucket).Put(key(book.ProviderID, book.ExternalID), b) })
}

func (s *Store) Get(provider, id string) (model.Book, bool, error) {
	var out entry
	err := s.db.View(func(tx *bolt.Tx) error {
		value := tx.Bucket(booksBucket).Get(key(provider, id))
		if value == nil {
			return os.ErrNotExist
		}
		return json.Unmarshal(value, &out)
	})
	if errors.Is(err, os.ErrNotExist) {
		return model.Book{}, false, nil
	}
	if err != nil {
		return model.Book{}, false, err
	}
	if time.Now().After(out.ExpiresAt) {
		_ = s.db.Update(func(tx *bolt.Tx) error { return tx.Bucket(booksBucket).Delete(key(provider, id)) })
		return model.Book{}, false, nil
	}
	return out.Book, true, nil
}

func (s *Store) Close() error { return s.db.Close() }
