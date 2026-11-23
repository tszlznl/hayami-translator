// Package lang contains language detection and automatic translation
// direction. It is pure logic and does not depend on the UI.
package lang

import "unicode"

// AutoTarget is the default target: Chinese source goes to English, every
// other source goes to Simplified Chinese.
const AutoTarget = "自动（中→英）"

// EffectiveTarget resolves the automatic choice into a concrete language.
// Picking any other target keeps that target for both directions.
func EffectiveTarget(target, source string) string {
	if target != AutoTarget && target != "自动检测" {
		return target
	}
	if IsChineseSource(source) {
		return "English"
	}
	return "简体中文"
}

// DetectName returns a friendly language name for display in the UI.
func DetectName(text string) string {
	if IsChineseSource(text) {
		return "中文"
	}
	var japanese, korean, cyrillic, arabic, thai, asciiLatin, extendedLatin int
	for _, r := range text {
		switch {
		case unicode.Is(unicode.Hiragana, r), unicode.Is(unicode.Katakana, r):
			japanese++
		case unicode.Is(unicode.Hangul, r):
			korean++
		case unicode.Is(unicode.Cyrillic, r):
			cyrillic++
		case unicode.Is(unicode.Arabic, r):
			arabic++
		case unicode.Is(unicode.Thai, r):
			thai++
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
			asciiLatin++
		case unicode.Is(unicode.Latin, r):
			extendedLatin++
		}
	}
	if japanese > 0 {
		return "日本語"
	}
	if korean > 0 {
		return "한국어"
	}
	if cyrillic > 0 {
		return "Русский"
	}
	if arabic > 0 {
		return "العربية"
	}
	if thai > 0 {
		return "ไทย"
	}
	if asciiLatin > 0 && extendedLatin == 0 {
		return "English"
	}
	return "自动检测"
}

// IsChineseSource is a local heuristic so the target is known before the
// request is sent. Kana or Hangul means Japanese/Korean, even when the text
// contains Han characters. Comparing Han characters against non-Han word runs
// keeps technical Chinese sentences with English identifiers (e.g. Kubernetes,
// microservice) recognized as Chinese while mostly English text with a trailing
// Han word still counts as non-Chinese.
func IsChineseSource(text string) bool {
	var han, otherWords int
	var kanaOrHangul, inOtherWord bool
	for _, r := range text {
		switch {
		case unicode.Is(unicode.Han, r):
			han++
			inOtherWord = false
		case unicode.Is(unicode.Hiragana, r),
			unicode.Is(unicode.Katakana, r),
			unicode.Is(unicode.Hangul, r):
			kanaOrHangul = true
			inOtherWord = false
		case unicode.IsLetter(r):
			if !inOtherWord {
				otherWords++
				inOtherWord = true
			}
		default:
			inOtherWord = false
		}
	}
	if han == 0 || kanaOrHangul {
		return false
	}
	return han >= otherWords
}
