package lang

import (
	"strings"
	"unicode"
)

const (
	maxWordInputBytes = 128
	maxWordBytes      = 64
	maxHyphens        = 3
	maxApostrophes    = 2
)

// IsSingleWord reports whether s is a single English lookup word (optionally
// hyphenated or contracted) and returns its normalized lowercase form.
// Multi-word phrases, sentences, code identifiers (camelCase/snake_case),
// numbers, URLs, paths, and non-ASCII/CJK inputs return ("", false) so they
// bypass the offline dictionary and stream directly from the LLM.
func IsSingleWord(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > maxWordInputBytes {
		return "", false
	}

	for _, r := range s {
		if unicode.IsSpace(r) {
			return "", false
		}
	}

	s = stripOuterPunctuation(s)
	if s == "" {
		return "", false
	}

	s = normalizeWordRunes(s)
	if len(s) == 0 || len(s) > maxWordBytes {
		return "", false
	}

	if s[0] == '-' || s[0] == '\'' || s[len(s)-1] == '-' || s[len(s)-1] == '\'' {
		return "", false
	}

	var hyphens, apostrophes int
	prevSep := false
	for i := 0; i < len(s); i++ {
		b := s[i]
		switch {
		case (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z'):
			prevSep = false
		case b == '-':
			if prevSep {
				return "", false
			}
			hyphens++
			if hyphens > maxHyphens {
				return "", false
			}
			prevSep = true
		case b == '\'':
			if prevSep {
				return "", false
			}
			apostrophes++
			if apostrophes > maxApostrophes {
				return "", false
			}
			prevSep = true
		default:
			return "", false
		}
	}

	if !validSegmentCasing(s) {
		return "", false
	}

	return strings.ToLower(s), true
}

func isWrapperRune(r rune) bool {
	switch r {
	case '\'', '"', '`',
		'‘', '’', '“', '”', '«', '»',
		'(', ')', '[', ']', '{', '}', '<', '>',
		'「', '」', '『', '』', '（', '）', '【', '】', '《', '》':
		return true
	default:
		return false
	}
}

func isTrailingSentencePunct(r rune) bool {
	switch r {
	case '.', ',', ';', ':', '!', '?', '…',
		'。', '，', '；', '：', '！', '？', '、':
		return true
	default:
		return false
	}
}

func stripOuterPunctuation(s string) string {
	for s != "" {
		prev := s
		s = strings.TrimFunc(s, isWrapperRune)
		s = strings.TrimRightFunc(s, isTrailingSentencePunct)
		s = strings.TrimFunc(s, isWrapperRune)
		if s == prev {
			break
		}
	}
	return s
}

func normalizeWordRunes(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '’', '‘':
			b.WriteByte('\'')
		case '‐', '‑':
			b.WriteByte('-')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func validSegmentCasing(s string) bool {
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == '-' || s[i] == '\'' {
			seg := s[start:i]
			if !validWordSegment(seg) {
				return false
			}
			start = i + 1
		}
	}
	return true
}

func validWordSegment(seg string) bool {
	if len(seg) == 0 {
		return false
	}
	allLower := true
	allUpper := true
	titleCase := seg[0] >= 'A' && seg[0] <= 'Z'
	for i := 0; i < len(seg); i++ {
		b := seg[i]
		isLower := b >= 'a' && b <= 'z'
		isUpper := b >= 'A' && b <= 'Z'
		if !isLower {
			allLower = false
			if i > 0 {
				titleCase = false
			}
		}
		if !isUpper {
			allUpper = false
		}
	}
	return allLower || allUpper || titleCase
}
