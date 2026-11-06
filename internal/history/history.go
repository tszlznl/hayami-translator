// Package history stores translation history in a local SQLite database.
// It has no UI dependency and can be tested with a temporary file.
package history

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/sqlite"
)

type Entry struct {
	ID        int64
	CreatedAt time.Time
	Source    string
	Result    string
	Target    string
	Mode      string
}

type Store struct {
	db *sqlite.DB
}

// Open opens history.db in the app's user data directory.
func Open() (*Store, error) {
	dir, err := mygo.App.Path(mygo.PathUserData)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return OpenAt(filepath.Join(dir, "history.db"))
}

func OpenAt(path string) (*Store, error) {
	db, err := sqlite.Open(context.Background(), path, sqlite.OpenOptions{})
	if err != nil {
		return nil, err
	}
	s := &Store{db: db}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := db.Execute(ctx, `CREATE TABLE IF NOT EXISTS history (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		created_at INTEGER NOT NULL,
		source TEXT NOT NULL,
		result TEXT NOT NULL,
		target TEXT NOT NULL,
		mode TEXT NOT NULL
	)`); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Add(e Entry) error {
	if s == nil || strings.TrimSpace(e.Result) == "" {
		return nil
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := s.db.Execute(ctx,
		`INSERT INTO history (created_at, source, result, target, mode) VALUES (?, ?, ?, ?, ?)`,
		e.CreatedAt.Unix(), e.Source, e.Result, e.Target, e.Mode)
	return err
}

func (s *Store) Search(query string, limit int) ([]Entry, error) {
	if s == nil {
		return nil, fmt.Errorf("历史数据库未打开")
	}
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	query = strings.TrimSpace(query)
	like := "%" + query + "%"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rows, err := s.db.Query(ctx, `SELECT id, created_at, source, result, target, mode
		FROM history
		WHERE ? = '' OR source LIKE ? OR result LIKE ?
		ORDER BY id DESC LIMIT ?`, query, like, like, limit)
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(rows.Values))
	for _, row := range rows.Values {
		if len(row) < 6 {
			continue
		}
		var e Entry
		e.ID, _ = row[0].(int64)
		sec, _ := row[1].(int64)
		e.CreatedAt = time.Unix(sec, 0)
		e.Source, _ = row[2].(string)
		e.Result, _ = row[3].(string)
		e.Target, _ = row[4].(string)
		e.Mode, _ = row[5].(string)
		out = append(out, e)
	}
	return out, nil
}

func (s *Store) Clear() error {
	if s == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := s.db.Execute(ctx, `DELETE FROM history`)
	return err
}

func (s *Store) Close() {
	if s != nil && s.db != nil {
		s.db.Close()
	}
}
