// Package dict provides an embedded, lazy-loaded offline English-Chinese
// dictionary derived from ECDICT.
package dict

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"io"
	"sort"
	"strings"
	"sync"
)

//go:embed dict_data.tsv.gz
var dictGZ []byte

// Meaning represents a single part-of-speech and definition line for a word.
type Meaning struct {
	POS string
	Def string
}

// Entry represents a dictionary headword with its IPA phonetic and meanings.
type Entry struct {
	Word     string
	Phonetic string
	Meanings []Meaning
}

var (
	initOnce sync.Once
	arena    string
	offsets  []uint32
	numWords int
)

func loadDict() {
	r, err := gzip.NewReader(bytes.NewReader(dictGZ))
	if err != nil {
		return
	}
	defer r.Close()

	var sb strings.Builder
	sb.Grow(3_800_000)
	if _, err := io.Copy(&sb, r); err != nil {
		return
	}

	arena = sb.String()
	offs := make([]uint32, 0, 58_300)
	offs = append(offs, 0)
	for i := 0; i < len(arena); i++ {
		if arena[i] == '\n' {
			offs = append(offs, uint32(i+1))
		}
	}
	if len(offs) > 1 {
		offsets = offs
		numWords = len(offs) - 1
	}
}

// Lookup searches the embedded dictionary for word (case-insensitive), falling
// back through possessive stripping, irregular lemma mapping, and regular
// English inflection stemming.
func Lookup(word string) (Entry, bool) {
	initOnce.Do(loadDict)
	if numWords == 0 {
		return Entry{}, false
	}

	w := strings.ToLower(strings.TrimSpace(word))
	if w == "" {
		return Entry{}, false
	}

	if e, ok := lookupExact(w); ok {
		return e, true
	}

	if strings.HasSuffix(w, "'s") && len(w) > 2 {
		if e, ok := lookupLemma(w[:len(w)-2]); ok {
			return e, true
		}
	} else if strings.HasSuffix(w, "'") && len(w) > 2 {
		if e, ok := lookupLemma(w[:len(w)-1]); ok {
			return e, true
		}
	}

	return lookupLemma(w)
}

func lookupLemma(w string) (Entry, bool) {
	if e, ok := lookupExact(w); ok {
		return e, true
	}

	if lemma, ok := irregularLemmas[w]; ok {
		if e, ok := lookupExact(lemma); ok {
			return e, true
		}
	}

	for _, cand := range candidateLemmas(w) {
		if e, ok := lookupExact(cand); ok {
			return e, true
		}
	}

	return Entry{}, false
}

func lineWord(i int) string {
	start := offsets[i]
	end := offsets[i+1] - 1
	line := arena[start:end]
	tab := strings.IndexByte(line, '\t')
	if tab < 0 {
		return line
	}
	return line[:tab]
}

func lookupExact(w string) (Entry, bool) {
	idx := sort.Search(numWords, func(i int) bool {
		return lineWord(i) >= w
	})
	if idx >= numWords || lineWord(idx) != w {
		return Entry{}, false
	}
	line := arena[offsets[idx] : offsets[idx+1]-1]
	return parseEntry(line), true
}

func parseEntry(line string) Entry {
	t1 := strings.IndexByte(line, '\t')
	if t1 < 0 {
		return Entry{Word: line}
	}
	word := line[:t1]
	rest := line[t1+1:]
	t2 := strings.IndexByte(rest, '\t')
	if t2 < 0 {
		return Entry{Word: word, Phonetic: rest}
	}
	phonetic := rest[:t2]
	rawTrans := rest[t2+1:]

	var meanings []Meaning
	for rawTrans != "" {
		var part string
		if sep := strings.Index(rawTrans, `\n`); sep >= 0 {
			part = rawTrans[:sep]
			rawTrans = rawTrans[sep+2:]
		} else {
			part = rawTrans
			rawTrans = ""
		}
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		meanings = append(meanings, parseMeaning(part))
	}

	return Entry{
		Word:     word,
		Phonetic: phonetic,
		Meanings: meanings,
	}
}

func parseMeaning(part string) Meaning {
	if strings.HasPrefix(part, "[") {
		if closeIdx := strings.IndexByte(part, ']'); closeIdx > 1 && closeIdx < 16 {
			pos := part[:closeIdx+1]
			def := strings.TrimSpace(part[closeIdx+1:])
			if def != "" {
				return Meaning{POS: pos, Def: def}
			}
		}
	}

	// Match ASCII POS prefix like "n. ", "vt. ", "interj. ", "v./n. " (1..10 bytes ending in '.').
	if spaceIdx := strings.IndexByte(part, ' '); spaceIdx >= 2 && spaceIdx <= 11 && part[spaceIdx-1] == '.' {
		prefix := part[:spaceIdx]
		if isASCIIPOS(prefix) {
			def := strings.TrimSpace(part[spaceIdx+1:])
			if def != "" {
				return Meaning{POS: prefix, Def: def}
			}
		}
	}

	return Meaning{Def: part}
}

func isASCIIPOS(s string) bool {
	hasLetter := false
	for i := 0; i < len(s); i++ {
		b := s[i]
		switch {
		case (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z'):
			hasLetter = true
		case b == '.' || b == '/':
		default:
			return false
		}
	}
	return hasLetter
}
