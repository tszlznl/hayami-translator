package main

import "hayami/internal/lang"

// The UI and prompt code keep their local names; the implementation lives in
// internal/lang so it can be tested without a window.
const autoTarget = lang.AutoTarget

func effectiveTarget(target, source string) string { return lang.EffectiveTarget(target, source) }

func detectLanguageName(text string) string { return lang.DetectName(text) }

func isChineseSource(text string) bool { return lang.IsChineseSource(text) }
