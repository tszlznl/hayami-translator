package cache

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestCacheHitMissAndKeySensitivity(t *testing.T) {
	c := New(16, DefaultTTL)
	base := Key{Source: "Apple", TargetLang: "简体中文", Mode: "translate", Service: "openai"}
	c.Set(base, Value{Result: "苹果", ProviderName: "OpenAI"})

	if v, ok := c.Get(base); !ok || v.Result != "苹果" || v.ProviderName != "OpenAI" {
		t.Fatalf("Get(base) = (%+v, %v), want hit", v, ok)
	}

	distinct := []Key{
		{Source: "apple", TargetLang: "简体中文", Mode: "translate", Service: "openai"},
		{Source: "Apple", TargetLang: "English", Mode: "translate", Service: "openai"},
		{Source: "Apple", TargetLang: "简体中文", Mode: "polish", Service: "openai"},
		{Source: "Apple", TargetLang: "简体中文", Mode: "translate", Service: "baidu"},
	}
	for _, k := range distinct {
		if _, ok := c.Get(k); ok {
			t.Errorf("Get(%+v) unexpectedly hit cache", k)
		}
	}
}

func TestCacheLRUEvictionAndUpdateInPlace(t *testing.T) {
	c := New(2, 0)
	kA := Key{Source: "A"}
	kB := Key{Source: "B"}
	kC := Key{Source: "C"}

	c.Set(kA, Value{Result: "1"})
	c.Set(kB, Value{Result: "2"})
	if _, ok := c.Get(kA); !ok {
		t.Fatal("kA should be present")
	}
	// kB is now LRU; inserting kC must evict kB.
	c.Set(kC, Value{Result: "3"})

	if c.Len() != 2 {
		t.Fatalf("Len() = %d, want 2", c.Len())
	}
	if _, ok := c.Get(kB); ok {
		t.Fatal("kB should have been evicted")
	}
	if _, ok := c.Get(kA); !ok {
		t.Fatal("kA should remain")
	}
	if _, ok := c.Get(kC); !ok {
		t.Fatal("kC should remain")
	}

	// Update in place should keep Len() == 2 and move kA to MRU.
	c.Set(kA, Value{Result: "1-updated"})
	if c.Len() != 2 {
		t.Fatalf("Len() after update = %d, want 2", c.Len())
	}
	if v, ok := c.Get(kA); !ok || v.Result != "1-updated" {
		t.Fatalf("updated kA = (%+v, %v)", v, ok)
	}
}

func TestCacheTTLExpiration(t *testing.T) {
	cur := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	c := NewWithClock(10, 5*time.Minute, func() time.Time { return cur })

	k := Key{Source: "hello"}
	c.Set(k, Value{Result: "你好"})

	cur = cur.Add(4*time.Minute + 59*time.Second)
	if v, ok := c.Get(k); !ok || v.Result != "你好" {
		t.Fatalf("Get at +4m59s = (%+v, %v), want hit", v, ok)
	}

	cur = cur.Add(1 * time.Second)
	if _, ok := c.Get(k); ok {
		t.Fatal("Get at +5m00s unexpectedly hit expired entry")
	}
	if c.Len() != 0 {
		t.Fatalf("expired entry should be evicted, Len() = %d", c.Len())
	}
}

func TestCacheDefensiveSliceCloningAndNilZeroSafety(t *testing.T) {
	// Nil safety
	var nilCache *TranslationCache
	if v, ok := nilCache.Get(Key{Source: "x"}); ok || v.Result != "" {
		t.Fatalf("nil Get = (%+v, %v)", v, ok)
	}
	nilCache.Set(Key{Source: "x"}, Value{Result: "y"})
	nilCache.Delete(Key{Source: "x"})
	nilCache.Clear()
	if nilCache.Len() != 0 {
		t.Fatalf("nil Len() = %d", nilCache.Len())
	}

	// Zero-value safety
	var zero TranslationCache
	k := Key{Source: "z"}
	snaps := []ProviderSnapshot{{ID: "p1", Name: "P1", Result: "orig"}}
	zero.Set(k, Value{Result: "ok", MultiResults: snaps})
	snaps[0].Result = "mutated-after-set"

	got, ok := zero.Get(k)
	if !ok || len(got.MultiResults) != 1 || got.MultiResults[0].Result != "orig" {
		t.Fatalf("Get after input mutation = (%+v, %v)", got, ok)
	}
	got.MultiResults[0].Result = "mutated-after-get"

	got2, ok := zero.Get(k)
	if !ok || got2.MultiResults[0].Result != "orig" {
		t.Fatalf("Get after output mutation = (%+v, %v)", got2, ok)
	}

	zero.Delete(k)
	if zero.Len() != 0 {
		t.Fatalf("Len() after Delete = %d", zero.Len())
	}
	zero.Set(k, Value{Result: "a"})
	zero.Clear()
	if zero.Len() != 0 {
		t.Fatalf("Len() after Clear = %d", zero.Len())
	}
}

func TestCacheConcurrency(t *testing.T) {
	c := New(64, time.Minute)
	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				k := Key{Source: fmt.Sprintf("word-%d", (id*7+i)%100)}
				c.Set(k, Value{
					Result:       "res",
					MultiResults: []ProviderSnapshot{{ID: "p1", Result: "res"}},
				})
				_, _ = c.Get(k)
				if i%25 == 0 {
					c.Delete(k)
				}
				_ = c.Len()
			}
		}(g)
	}
	wg.Wait()
}
