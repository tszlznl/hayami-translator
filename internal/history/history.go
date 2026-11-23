// Package history stores translation history in a local SQLite database with
// an FTS5 full-text search index, retention pruning, and Markdown/CSV export.
// It has no UI dependency and can be tested with a temporary file.
package history

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/sqlite"
)

const DefaultMaxEntries = 1000

// FollowUpTurn records one completed follow-up Q&A pair attached to a history entry.
type FollowUpTurn struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

type Entry struct {
	ID        int64
	CreatedAt time.Time
	Source    string
	Result    string
	Target    string
	Mode      string
	FollowUps []FollowUpTurn
}

type Store struct {
	mu         sync.RWMutex
	db         *sqlite.DB
	maxEntries int
	closed     bool
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
	s := &Store{db: db, maxEntries: DefaultMaxEntries}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.initSchema(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) initSchema(ctx context.Context) error {
	if _, err := s.db.Execute(ctx, `CREATE TABLE IF NOT EXISTS history (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		created_at INTEGER NOT NULL,
		source TEXT NOT NULL,
		result TEXT NOT NULL,
		target TEXT NOT NULL,
		mode TEXT NOT NULL,
		follow_ups TEXT NOT NULL DEFAULT '[]'
	)`); err != nil {
		return err
	}
	// Migrate older databases created before follow_ups existed.
	hasFollowUps, err := s.columnExists(ctx, "history", "follow_ups")
	if err != nil {
		return err
	}
	if !hasFollowUps {
		if _, err := s.db.Execute(ctx, `ALTER TABLE history ADD COLUMN follow_ups TEXT NOT NULL DEFAULT '[]'`); err != nil {
			return err
		}
	}

	// Create FTS5 virtual table and sync triggers.
	if _, err := s.db.Execute(ctx, `CREATE VIRTUAL TABLE IF NOT EXISTS history_fts USING fts5(
		source,
		result,
		follow_ups_text,
		tokenize = 'unicode61'
	)`); err != nil {
		return err
	}
	for _, trigSQL := range []string{
		`CREATE TRIGGER IF NOT EXISTS history_ai AFTER INSERT ON history BEGIN
			INSERT INTO history_fts(rowid, source, result, follow_ups_text)
			VALUES (new.id, new.source, new.result, new.follow_ups);
		END`,
		`CREATE TRIGGER IF NOT EXISTS history_ad AFTER DELETE ON history BEGIN
			DELETE FROM history_fts WHERE rowid = old.id;
		END`,
		`CREATE TRIGGER IF NOT EXISTS history_au AFTER UPDATE ON history BEGIN
			DELETE FROM history_fts WHERE rowid = old.id;
			INSERT INTO history_fts(rowid, source, result, follow_ups_text)
			VALUES (new.id, new.source, new.result, new.follow_ups);
		END`,
	} {
		if _, err := s.db.Execute(ctx, trigSQL); err != nil {
			return err
		}
	}

	// Backfill any pre-existing rows into history_fts.
	if _, err := s.db.Execute(ctx, `INSERT INTO history_fts(rowid, source, result, follow_ups_text)
		SELECT h.id, h.source, h.result, h.follow_ups
		FROM history h
		WHERE NOT EXISTS (SELECT 1 FROM history_fts f WHERE f.rowid = h.id)`); err != nil {
		return err
	}
	return nil
}

func (s *Store) columnExists(ctx context.Context, table, col string) (bool, error) {
	rows, err := s.db.Query(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return false, err
	}
	for _, r := range rows.Values {
		if len(r) >= 2 {
			if name, ok := r[1].(string); ok && strings.EqualFold(name, col) {
				return true, nil
			}
		}
	}
	return false, nil
}

// SetMaxEntries updates the retention cap and immediately prunes excess oldest
// rows when maxEntries > 0.
func (s *Store) SetMaxEntries(maxEntries int) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	s.maxEntries = maxEntries
	s.mu.Unlock()
	if maxEntries <= 0 {
		return nil
	}
	return s.Prune(maxEntries)
}

// Prune deletes the oldest history entries so at most maxEntries remain.
func (s *Store) Prune(maxEntries int) error {
	if s == nil || maxEntries <= 0 {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed || s.db == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.pruneLocked(ctx, maxEntries)
}

func (s *Store) pruneLocked(ctx context.Context, maxEntries int) error {
	if maxEntries <= 0 {
		return nil
	}
	_, err := s.db.Execute(ctx,
		`DELETE FROM history WHERE id NOT IN (SELECT id FROM history ORDER BY id DESC LIMIT ?)`,
		maxEntries)
	return err
}

func marshalFollowUps(turns []FollowUpTurn) string {
	if len(turns) == 0 {
		return "[]"
	}
	b, err := json.Marshal(turns)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func unmarshalFollowUps(raw string) []FollowUpTurn {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "[]" || raw == "null" {
		return nil
	}
	var turns []FollowUpTurn
	if err := json.Unmarshal([]byte(raw), &turns); err != nil {
		return nil
	}
	return turns
}

// Add inserts a history entry and returns its assigned database row ID.
func (s *Store) Add(e Entry) error {
	_, err := s.AddReturningID(e)
	return err
}

// AddReturningID inserts a history entry, enforces the retention limit, and
// returns the new row ID.
func (s *Store) AddReturningID(e Entry) (int64, error) {
	if s == nil || strings.TrimSpace(e.Result) == "" {
		return 0, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed || s.db == nil {
		return 0, nil
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := s.db.Execute(ctx,
		`INSERT INTO history (created_at, source, result, target, mode, follow_ups) VALUES (?, ?, ?, ?, ?, ?)`,
		e.CreatedAt.Unix(), e.Source, e.Result, e.Target, e.Mode, marshalFollowUps(e.FollowUps))
	if err != nil {
		return 0, err
	}
	if s.maxEntries > 0 {
		_ = s.pruneLocked(ctx, s.maxEntries)
	}
	return res.LastInsertID, nil
}

// UpdateFollowUps updates the persisted follow-up turns of an existing history row.
func (s *Store) UpdateFollowUps(id int64, turns []FollowUpTurn) error {
	if s == nil || id <= 0 {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed || s.db == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := s.db.Execute(ctx,
		`UPDATE history SET follow_ups = ? WHERE id = ?`,
		marshalFollowUps(turns), id)
	return err
}

func escapeLikePattern(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// buildFTS5MatchQuery turns a user query into a safe FTS5 MATCH expression
// when the query consists of alphanumeric/CJK tokens, or returns ("", false)
// when punctuation/wildcards require exact LIKE substring matching.
func buildFTS5MatchQuery(query string) (string, bool) {
	fields := strings.Fields(query)
	if len(fields) == 0 {
		return "", false
	}
	parts := make([]string, 0, len(fields))
	for _, f := range fields {
		hasWord := false
		for _, r := range f {
			if unicode.IsLetter(r) || unicode.IsNumber(r) {
				hasWord = true
				continue
			}
			// Punctuation or symbols like %, _, \, quotes: let LIKE handle exact substring.
			return "", false
		}
		if !hasWord {
			return "", false
		}
		escaped := strings.ReplaceAll(f, `"`, `""`)
		parts = append(parts, `"`+escaped+`"*`)
	}
	return strings.Join(parts, " AND "), true
}

func (s *Store) Search(query string, limit int) ([]Entry, error) {
	if s == nil {
		return nil, fmt.Errorf("历史数据库未打开")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed || s.db == nil {
		return nil, fmt.Errorf("历史数据库已关闭")
	}
	if limit <= 0 || limit > 5000 {
		limit = 200
	}
	query = strings.TrimSpace(query)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if query == "" {
		rows, err := s.db.Query(ctx, `SELECT id, created_at, source, result, target, mode, follow_ups
			FROM history ORDER BY id DESC LIMIT ?`, limit)
		if err != nil {
			return nil, err
		}
		return decodeRows(rows), nil
	}

	if ftsExpr, ok := buildFTS5MatchQuery(query); ok {
		rows, err := s.db.Query(ctx, `SELECT h.id, h.created_at, h.source, h.result, h.target, h.mode, h.follow_ups
			FROM history h
			JOIN history_fts f ON h.id = f.rowid
			WHERE history_fts MATCH ?
			ORDER BY h.id DESC LIMIT ?`, ftsExpr, limit)
		if err == nil && len(rows.Values) > 0 {
			return decodeRows(rows), nil
		}
	}

	like := "%" + escapeLikePattern(query) + "%"
	rows, err := s.db.Query(ctx, `SELECT id, created_at, source, result, target, mode, follow_ups
		FROM history
		WHERE source LIKE ? ESCAPE '\' OR result LIKE ? ESCAPE '\' OR follow_ups LIKE ? ESCAPE '\'
		ORDER BY id DESC LIMIT ?`, like, like, like, limit)
	if err != nil {
		return nil, err
	}
	return decodeRows(rows), nil
}

func decodeRows(rows sqlite.Rows) []Entry {
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
		if len(row) >= 7 {
			rawFU, _ := row[6].(string)
			e.FollowUps = unmarshalFollowUps(rawFU)
		}
		out = append(out, e)
	}
	return out
}

func (s *Store) Count() (int, error) {
	if s == nil {
		return 0, fmt.Errorf("历史数据库未打开")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed || s.db == nil {
		return 0, fmt.Errorf("历史数据库已关闭")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rows, err := s.db.Query(ctx, `SELECT COUNT(*) FROM history`)
	if err != nil {
		return 0, err
	}
	if len(rows.Values) == 0 || len(rows.Values[0]) == 0 {
		return 0, nil
	}
	n, _ := rows.Values[0][0].(int64)
	return int(n), nil
}

// IntegrityCheck runs SQLite's PRAGMA integrity_check on the database.
func (s *Store) IntegrityCheck() error {
	if s == nil {
		return fmt.Errorf("历史数据库未打开")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed || s.db == nil {
		return fmt.Errorf("历史数据库已关闭")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rows, err := s.db.Query(ctx, `PRAGMA integrity_check`)
	if err != nil {
		return err
	}
	if len(rows.Values) == 0 || len(rows.Values[0]) == 0 {
		return fmt.Errorf("integrity_check 未返回结果")
	}
	res, _ := rows.Values[0][0].(string)
	if !strings.EqualFold(strings.TrimSpace(res), "ok") {
		return fmt.Errorf("数据库完整性校验失败：%s", res)
	}
	return nil
}

func (s *Store) Clear() error {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed || s.db == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := s.db.Execute(ctx, `DELETE FROM history`)
	return err
}

func (s *Store) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	if s.db != nil {
		s.db.Close()
		s.db = nil
	}
}

// ExportMarkdown formats the given entries as a structured Markdown document.
func ExportMarkdown(entries []Entry) string {
	var b strings.Builder
	b.WriteString("# Hayami Translator 历史记录\n\n")
	for i, e := range entries {
		if i > 0 {
			b.WriteString("\n---\n\n")
		}
		ts := e.CreatedAt.Format("2006-01-02 15:04:05")
		fmt.Fprintf(&b, "## %s · %s (%s)\n\n", ts, e.Target, e.Mode)
		b.WriteString("### 原文\n\n")
		b.WriteString(strings.TrimSpace(e.Source))
		b.WriteString("\n\n### 译文\n\n")
		b.WriteString(strings.TrimSpace(e.Result))
		b.WriteString("\n")
		if len(e.FollowUps) > 0 {
			b.WriteString("\n### 追问记录\n")
			for j, turn := range e.FollowUps {
				fmt.Fprintf(&b, "\n**Q%d：%s**\n\n%s\n", j+1, strings.TrimSpace(turn.Question), strings.TrimSpace(turn.Answer))
			}
		}
	}
	return b.String()
}

// ExportCSV formats the given entries as RFC 4180 CSV with UTF-8 encoding.
func ExportCSV(entries []Entry) (string, error) {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if err := w.Write([]string{"id", "created_at", "mode", "target", "source", "result", "follow_ups"}); err != nil {
		return "", err
	}
	for _, e := range entries {
		ts := ""
		if !e.CreatedAt.IsZero() {
			ts = e.CreatedAt.Format(time.RFC3339)
		}
		row := []string{
			fmt.Sprintf("%d", e.ID),
			ts,
			e.Mode,
			e.Target,
			e.Source,
			e.Result,
			marshalFollowUps(e.FollowUps),
		}
		if err := w.Write(row); err != nil {
			return "", err
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return "", err
	}
	return buf.String(), nil
}
