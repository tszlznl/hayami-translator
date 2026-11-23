package dict

import (
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestLookupExactAndNormalization(t *testing.T) {
	tests := []struct {
		query    string
		wantWord string
		wantIPA  bool
	}{
		{"hello", "hello", true},
		{"  HELLO ", "hello", true},
		{"Algorithm", "algorithm", true},
		{"serendipity", "serendipity", true},
		{"ephemeral", "ephemeral", true},
		{"well-known", "well-known", false},
		{"don't", "don't", true},
	}

	for _, tc := range tests {
		entry, ok := Lookup(tc.query)
		if !ok {
			t.Fatalf("Lookup(%q) = false, want true", tc.query)
		}
		if entry.Word != tc.wantWord {
			t.Errorf("Lookup(%q).Word = %q, want %q", tc.query, entry.Word, tc.wantWord)
		}
		if len(entry.Meanings) == 0 {
			t.Errorf("Lookup(%q).Meanings is empty", tc.query)
		}
		if tc.wantIPA && entry.Phonetic == "" {
			t.Errorf("Lookup(%q).Phonetic is empty", tc.query)
		}
		if strings.Contains(entry.Phonetic, "ә") || strings.Contains(entry.Phonetic, "^") || strings.Contains(entry.Phonetic, "(?@)") {
			t.Errorf("Lookup(%q).Phonetic has unnormalized legacy tokens: %q", tc.query, entry.Phonetic)
		}
	}

	alg, ok := Lookup("algorithm")
	if !ok {
		t.Fatal("algorithm missing")
	}
	if alg.Meanings[0].POS != "n." {
		t.Errorf("algorithm.Meanings[0].POS = %q, want n.", alg.Meanings[0].POS)
	}
}

func TestLookupInflectionsAndIrregulars(t *testing.T) {
	tests := []struct {
		query    string
		wantWord string
	}{
		// Regular plurals (not direct core entries -> exercises stemming)
		{"algorithms", "algorithm"},
		{"categories", "category"},
		{"boxes", "box"},
		{"watches", "watch"},
		{"fishes", "fish"},
		{"wolves", "wolf"},
		{"loses", "lose"},
		{"cases", "case"},
		{"noses", "nose"},
		{"poses", "pose"},
		{"doses", "dose"},
		{"roses", "rose"},
		{"chases", "chase"},
		{"clauses", "clause"},
		{"gazes", "gaze"},
		{"grazes", "graze"},
		{"floes", "floe"},
		{"buses", "bus"},
		{"gases", "gas"},
		{"lenses", "lens"},
		{"aliases", "alias"},
		{"statuses", "status"},
		{"losses", "loss"},
		{"passes", "pass"},
		{"kisses", "kiss"},
		{"classes", "class"},
		{"blitzes", "blitz"},
		{"waltzes", "waltz"},
		// Inflected verbs / gerunds / comparatives (non-core -> exercises stemming)
		{"debugging", "debug"},
		{"hoping", "hope"},
		{"using", "use"},
		{"acing", "ace"},
		{"aping", "ape"},
		{"bigger", "big"},
		{"biggest", "big"},
		{"hottest", "hot"},
		{"saddest", "sad"},
		{"redder", "red"},
		{"reddest", "red"},
		{"oddest", "odd"},
		{"easiest", "easy"},
		{"larger", "large"},
		{"widest", "wide"},
		{"nicer", "nice"},
		{"finer", "fine"},
		{"braver", "brave"},
		{"rarest", "rare"},
		{"strangest", "strange"},
		{"whiter", "white"},
		{"bluer", "blue"},
		{"cuter", "cute"},
		{"huger", "huge"},
		{"paler", "pale"},
		{"purer", "pure"},
		{"riper", "ripe"},
		{"looser", "loose"},
		{"denser", "dense"},
		{"fiercer", "fierce"},
		{"older", "old"},
		{"oldest", "old"},
		{"darker", "dark"},
		{"darkest", "dark"},
		{"poorer", "poor"},
		{"poorest", "poor"},
		{"sharper", "sharp"},
		{"sharpest", "sharp"},
		{"wildest", "wild"},
		{"greener", "green"},
		{"greenest", "green"},
		{"localer", "local"},
		{"auguster", "august"},
		{"taller", "tall"},
		{"tallest", "tall"},
		{"fullest", "full"},
		{"griller", "grill"},
		{"smaller", "small"},
		{"aches", "ache"},
		{"eked", "eke"},
		{"sicked", "sick"},
		{"sicking", "sick"},
		{"flatting", "flat"},
		{"feeing", "fee"},
		{"heroes", "hero"},
		{"potatoes", "potato"},
		{"tomatoes", "tomato"},
		{"vetoes", "veto"},
		{"torpedoes", "torpedo"},
		{"mosquitoes", "mosquito"},
		{"volcanoes", "volcano"},
		{"echoes", "echo"},
		{"goes", "go"},
		{"quizzes", "quiz"},
		{"buzzes", "buzz"},
		// Possessives (exact, irregular, and stemmed bases)
		{"algorithm's", "algorithm"},
		{"mice's", "mouse"},
		{"students'", "student"},
		{"cats'", "cat"},
		{"wolves'", "wolf"},
		// Irregulars & direct inflected forms
		{"went", "go"},
		{"ate", "eat"},
		{"mice", "mouse"},
		{"geese", "goose"},
		{"bought", "buy"},
		{"better", "better"},
	}

	for _, tc := range tests {
		entry, ok := Lookup(tc.query)
		if !ok {
			t.Fatalf("Lookup(%q) = false, want true", tc.query)
		}
		if entry.Word != tc.wantWord {
			t.Errorf("Lookup(%q).Word = %q, want %q", tc.query, entry.Word, tc.wantWord)
		}
		if len(entry.Meanings) == 0 {
			t.Errorf("Lookup(%q).Meanings is empty", tc.query)
		}
	}

	// Verify cleaned ECDICT artifacts no longer map to wrong headwords.
	if e, ok := Lookup("ramen"); ok && e.Word == "raman" {
		t.Fatalf("Lookup(%q) erroneously mapped to %q", "ramen", e.Word)
	}
	for _, bogus := range []struct {
		query string
		wrong string
	}{
		{"boing", "bo"},
		{"brer", "br"},
		{"geest", "gee"},
		{"het", "heat"},
		{"kier", "ky"},
		{"nuer", "nu"},
		{"oxer", "ox"},
		{"oyer", "oy"},
		{"sked", "sk"},
		{"zed", "ze"},
	} {
		if e, ok := Lookup(bogus.query); ok && e.Word == bogus.wrong {
			t.Errorf("Lookup(%q) erroneously mapped to %q", bogus.query, e.Word)
		}
	}

	// Direct entries that exist as inflected headwords in Core 58k
	for _, w := range []string{"added", "hated", "stopped", "studied", "making", "lying", "happily", "simply", "children", "wider", "later", "does"} {
		if entry, ok := Lookup(w); !ok || entry.Word != w || len(entry.Meanings) == 0 {
			t.Errorf("Lookup(%q) failed: ok=%v entry=%+v", w, ok, entry)
		}
	}
}

func TestCandidateLemmasOrder(t *testing.T) {
	indexOf := func(list []string, target string) int {
		for i, v := range list {
			if v == target {
				return i
			}
		}
		return -1
	}

	cases := []struct {
		word   string
		want   string
		before string
	}{
		{"added", "add", "ad"},
		{"hated", "hate", "hat"},
		{"hoping", "hope", "hop"},
		{"stopped", "stop", ""},
		{"studied", "study", ""},
		{"making", "make", ""},
		{"lying", "lie", ""},
		{"happily", "happy", ""},
		{"simply", "simple", ""},
		{"loses", "lose", "los"},
		{"cases", "case", "cas"},
		{"noses", "nose", "nos"},
		{"poses", "pose", "pos"},
		{"doses", "dose", "dos"},
		{"roses", "rose", "ros"},
		{"chases", "chase", "chas"},
		{"clauses", "clause", "claus"},
		{"gazes", "gaze", "gaz"},
		{"grazes", "graze", "graz"},
		{"floes", "floe", "flo"},
		{"buses", "bus", "buse"},
		{"gases", "gas", "gase"},
		{"lenses", "lens", "lense"},
		{"aliases", "alias", "aliase"},
		{"losses", "loss", "los"},
		{"passes", "pass", "pas"},
		{"heroes", "hero", ""},
		{"potatoes", "potato", ""},
		{"bigger", "big", ""},
		{"redder", "red", "redd"},
		{"reddest", "red", "redd"},
		{"easiest", "easy", ""},
		{"older", "old", "olde"},
		{"oldest", "old", "olde"},
		{"darker", "dark", "darke"},
		{"darkest", "dark", "darke"},
		{"poorer", "poor", "poore"},
		{"poorest", "poor", "poore"},
		{"sharper", "sharp", "sharpe"},
		{"sharpest", "sharp", "sharpe"},
		{"wildest", "wild", "wilde"},
		{"greener", "green", "greene"},
		{"greenest", "green", "greene"},
		{"wider", "wide", "wid"},
		{"braver", "brave", "brav"},
		{"later", "late", "lat"},
		{"nicer", "nice", "nic"},
		{"finer", "fine", "fin"},
		{"larger", "large", "larg"},
		{"strangest", "strange", "strang"},
		{"whiter", "white", "whit"},
		{"bluer", "blue", "blu"},
		{"cuter", "cute", "cut"},
		{"huger", "huge", "hug"},
		{"paler", "pale", "pal"},
		{"purer", "pure", "pur"},
		{"riper", "ripe", "rip"},
		{"taller", "tall", "tal"},
		{"tallest", "tall", "tal"},
		{"fullest", "full", "ful"},
		{"griller", "grill", "gril"},
		{"buzzes", "buzz", "buz"},
		{"quizzes", "quiz", ""},
		{"using", "use", "us"},
	}
	for _, tc := range cases {
		cands := candidateLemmas(tc.word)
		idx := indexOf(cands, tc.want)
		if idx < 0 {
			t.Errorf("candidateLemmas(%q) = %v, missing %q", tc.word, cands, tc.want)
			continue
		}
		if tc.before != "" {
			if badIdx := indexOf(cands, tc.before); badIdx >= 0 && badIdx < idx {
				t.Errorf("candidateLemmas(%q) = %v, %q appeared at %d before %q at %d", tc.word, cands, tc.before, badIdx, tc.want, idx)
			}
		}
	}
}

func TestLookupMisses(t *testing.T) {
	misses := []string{"", "   ", "你好", "hello world", "xyzzyplugh123", "'", "''", "'s", "s'", "ramen"}
	for _, m := range misses {
		if e, ok := Lookup(m); ok {
			t.Errorf("Lookup(%q) = (%+v, true), want (zero, false)", m, e)
		}
	}
}

func TestLookupConcurrent(t *testing.T) {
	words := []string{"hello", "algorithm", "went", "debugging", "mice", "xyzzyplugh123"}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				w := words[(id+j)%len(words)]
				e, ok := Lookup(w)
				if w == "xyzzyplugh123" {
					if ok {
						t.Errorf("unexpected hit for %q", w)
					}
					continue
				}
				if !ok || len(e.Meanings) == 0 {
					t.Errorf("concurrent Lookup(%q) failed: ok=%v", w, ok)
				}
			}
		}(i)
	}
	wg.Wait()
}

func TestDictMemoryFootprint(t *testing.T) {
	// Ensure dictionary is initialized, then inspect heap and verify total
	// dictionary structures stay well within the 12 MiB budget.
	if _, ok := Lookup("hello"); !ok {
		t.Fatal("expected hello in dictionary")
	}
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	approxBytes := int64(len(arena)) + int64(len(offsets))*4
	if approxBytes > 12*1024*1024 {
		t.Fatalf("dictionary resident structures = %d bytes, want < 12 MiB", approxBytes)
	}
	if ms.HeapAlloc > 100*1024*1024 {
		t.Fatalf("unexpectedly high HeapAlloc after dict load: %d bytes", ms.HeapAlloc)
	}
}

func BenchmarkLookup(b *testing.B) {
	_, _ = Lookup("hello")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = Lookup("algorithm")
	}
}
