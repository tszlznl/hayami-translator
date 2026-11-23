package history

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/plugins/sqlite"
)

func TestSearchEscapesLikeWildcardsAndCountMatchesTotal(t *testing.T) {
	s, err := OpenAt(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	entries := []Entry{
		{CreatedAt: time.Now(), Source: "discount 50% off", Result: "五折优惠", Target: "简体中文", Mode: "translate"},
		{CreatedAt: time.Now(), Source: "snake_case_var", Result: "蛇形命名", Target: "简体中文", Mode: "translate"},
		{CreatedAt: time.Now(), Source: "plain hello", Result: "你好", Target: "简体中文", Mode: "translate"},
		{CreatedAt: time.Now(), Source: `path\to\file`, Result: "文件路径", Target: "简体中文", Mode: "translate"},
	}
	for _, e := range entries {
		if err := s.Add(e); err != nil {
			t.Fatal(err)
		}
	}

	total, err := s.Count()
	if err != nil || total != 4 {
		t.Fatalf("Count() = (%d, %v), want (4, nil)", total, err)
	}

	// Limited search still reflects total in Count().
	limited, err := s.Search("", 2)
	if err != nil || len(limited) != 2 {
		t.Fatalf("limited Search = (%d, %v)", len(limited), err)
	}

	pct, err := s.Search("%", 50)
	if err != nil || len(pct) != 1 || pct[0].Source != "discount 50% off" {
		t.Fatalf("Search('%%') = %#v, err=%v", pct, err)
	}
	us, err := s.Search("_", 50)
	if err != nil || len(us) != 1 || us[0].Source != "snake_case_var" {
		t.Fatalf("Search('_') = %#v, err=%v", us, err)
	}
	bs, err := s.Search(`\`, 50)
	if err != nil || len(bs) != 1 || bs[0].Source != `path\to\file` {
		t.Fatalf("Search('\\') = %#v, err=%v", bs, err)
	}
}

func TestStoreConcurrentAccessAndCloseSafety(t *testing.T) {
	s, err := OpenAt(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for range 15 {
				_ = s.Add(Entry{Source: "hello", Result: "你好", Target: "简体中文", Mode: "translate"})
				_, _ = s.Search("hello", 10)
				_, _ = s.Count()
			}
		}(i)
	}

	time.Sleep(5 * time.Millisecond)
	s.Close()
	// Second Close must be a safe no-op.
	s.Close()
	wg.Wait()

	if _, err := s.Search("", 10); err == nil {
		t.Fatal("Search after Close should return an error")
	}
	if _, err := s.Count(); err == nil {
		t.Fatal("Count after Close should return an error")
	}
}

func TestFTS5MigrationFollowUpsRetentionAndExport(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy.db")

	// 1. Seed a legacy table without follow_ups or history_fts.
	ctx := context.Background()
	rawDB, err := sqlite.Open(ctx, dbPath, sqlite.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rawDB.Execute(ctx, `CREATE TABLE history (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		created_at INTEGER NOT NULL,
		source TEXT NOT NULL,
		result TEXT NOT NULL,
		target TEXT NOT NULL,
		mode TEXT NOT NULL
	)`); err != nil {
		rawDB.Close()
		t.Fatal(err)
	}
	if _, err := rawDB.Execute(ctx, `INSERT INTO history (created_at, source, result, target, mode) VALUES (?, ?, ?, ?, ?)`,
		1700000000, "kubernetes deployment", "K8s 部署指南", "简体中文", "translate"); err != nil {
		rawDB.Close()
		t.Fatal(err)
	}
	rawDB.Close()

	// 2. OpenAt must auto-migrate follow_ups column and backfill history_fts.
	s, err := OpenAt(dbPath)
	if err != nil {
		t.Fatalf("OpenAt on legacy db: %v", err)
	}
	defer s.Close()

	if err := s.IntegrityCheck(); err != nil {
		t.Fatalf("IntegrityCheck: %v", err)
	}

	hits, err := s.Search("kubernetes", 20)
	if err != nil || len(hits) != 1 || hits[0].Result != "K8s 部署指南" {
		t.Fatalf("FTS5 search on migrated row = %#v, err=%v", hits, err)
	}

	// 3. UpdateFollowUps and search inside follow-up content.
	turns := []FollowUpTurn{
		{Question: "再简短一点", Answer: "精简版 K8s 指南"},
		{Question: "加个 YAML 示例", Answer: "replicas: 3"},
	}
	if err := s.UpdateFollowUps(hits[0].ID, turns); err != nil {
		t.Fatalf("UpdateFollowUps: %v", err)
	}
	fuHits, err := s.Search("replicas", 20)
	if err != nil || len(fuHits) != 1 || len(fuHits[0].FollowUps) != 2 {
		t.Fatalf("search follow-up text = %#v, err=%v", fuHits, err)
	}

	// 4. Retention pruning via SetMaxEntries.
	for i := 0; i < 5; i++ {
		if _, err := s.AddReturningID(Entry{
			CreatedAt: time.Unix(int64(1700000100+i), 0),
			Source:    "entry",
			Result:    "结果",
			Target:    "简体中文",
			Mode:      "translate",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetMaxEntries(3); err != nil {
		t.Fatalf("SetMaxEntries(3): %v", err)
	}
	cnt, err := s.Count()
	if err != nil || cnt != 3 {
		t.Fatalf("after SetMaxEntries(3), Count() = (%d, %v), want 3", cnt, err)
	}

	// 5. ExportMarkdown and ExportCSV.
	exportEntries := []Entry{
		{
			ID:        1,
			CreatedAt: time.Unix(1700000000, 0).UTC(),
			Source:    "hello, \"world\"",
			Result:    "你好，世界",
			Target:    "简体中文",
			Mode:      "translate",
			FollowUps: turns,
		},
	}
	md := ExportMarkdown(exportEntries)
	for _, want := range []string{"# Hayami Translator 历史记录", "hello, \"world\"", "你好，世界", "再简短一点", "replicas: 3"} {
		if !strings.Contains(md, want) {
			t.Fatalf("ExportMarkdown missing %q:\n%s", want, md)
		}
	}
	csvText, err := ExportCSV(exportEntries)
	if err != nil {
		t.Fatalf("ExportCSV: %v", err)
	}
	for _, want := range []string{"id,created_at,mode,target,source,result,follow_ups", "你好，世界", "replicas: 3"} {
		if !strings.Contains(csvText, want) {
			t.Fatalf("ExportCSV missing %q:\n%s", want, csvText)
		}
	}
}
