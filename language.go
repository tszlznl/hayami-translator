package main

import "unicode"

// autoTarget is the default target: Chinese source goes to English, every
// other source goes to Simplified Chinese.
const autoTarget = "自动（中→英）"

// effectiveTarget resolves the automatic choice into a concrete language.
// Picking any other target keeps that target for both directions.
func effectiveTarget(target, source string) string {
	if target != autoTarget {
		return target
	}
	if isChineseSource(source) {
		return "English"
	}
	return "简体中文"
}

// isChineseSource is a local heuristic so the target is known before the
// request is sent. Kana or Hangul means Japanese/Korean, even when the text
// contains Han characters. Mixed Chinese with a few English terms still
// counts as Chinese.
func isChineseSource(text string) bool {
	var han, letters int
	var kanaOrHangul bool
	for _, r := range text {
		switch {
		case unicode.Is(unicode.Han, r):
			han++
		case unicode.Is(unicode.Hiragana, r),
			unicode.Is(unicode.Katakana, r),
			unicode.Is(unicode.Hangul, r):
			kanaOrHangul = true
		case unicode.IsLetter(r):
			letters++
		}
	}
	if han == 0 || kanaOrHangul {
		return false
	}
	return han*2 >= letters
}
