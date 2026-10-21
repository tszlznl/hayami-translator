package main

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

type historyEntry struct {
	ID        int64
	CreatedAt time.Time
	Source    string
	Result    string
	Target    string
	Mode      string
}

type historyStore struct {
	db *sqlite.DB
}

func openHistory() (*historyStore, error) {
	dir, err := mygo.App.Path(mygo.PathUserData)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return openHistoryAt(filepath.Join(dir, "history.db"))
}

func openHistoryAt(path string) (*historyStore, error) {
	db, err := sqlite.Open(context.Background(), path, sqlite.OpenOptions{})
	if err != nil {
		return nil, err
	}
	s := &historyStore{db: db}
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

func (s *historyStore) add(e historyEntry) error {
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

func (s *historyStore) search(query string, limit int) ([]historyEntry, error) {
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
	out := make([]historyEntry, 0, len(rows.Values))
	for _, row := range rows.Values {
		if len(row) < 6 {
			continue
		}
		var e historyEntry
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

func (s *historyStore) clear() error {
	if s == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := s.db.Execute(ctx, `DELETE FROM history`)
	return err
}

func (s *historyStore) close() {
	if s != nil && s.db != nil {
		s.db.Close()
	}
}
