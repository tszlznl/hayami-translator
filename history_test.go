package main

import (
	"path/filepath"
	"testing"
	"time"
)

func TestHistoryStore(t *testing.T) {
	s, err := openHistoryAt(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	now := time.Now()
	if err := s.Add(historyEntry{CreatedAt: now, Source: "Hello world", Result: "你好，世界", Target: "简体中文", Mode: "text"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Add(historyEntry{CreatedAt: now, Source: "Screenshot", Result: "截图", Target: "简体中文", Mode: "image"}); err != nil {
		t.Fatal(err)
	}

	all, err := s.Search("", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].Source != "Screenshot" {
		t.Fatalf("entries %#v", all)
	}

	hits, err := s.Search("Hello", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Result != "你好，世界" || hits[0].Mode != "text" {
		t.Fatalf("hits %#v", hits)
	}

	if err := s.Clear(); err != nil {
		t.Fatal(err)
	}
	left, err := s.Search("", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Fatalf("after clear %#v", left)
	}
}
