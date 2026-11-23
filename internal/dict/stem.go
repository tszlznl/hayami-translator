package dict

import "strings"

// candidateLemmas returns plausible base forms for an inflected English word,
// ordered from most specific to most general.
func candidateLemmas(w string) []string {
	var out []string

	// Plural / 3rd-person singular (-s / -es)
	if strings.HasSuffix(w, "ies") && len(w) > 4 {
		out = append(out, w[:len(w)-3]+"y")
	}
	if strings.HasSuffix(w, "ves") && len(w) > 4 {
		base := w[:len(w)-3]
		out = append(out, base+"f", base+"fe")
	}
	matchedDoubledOrCluster := false
	emittedSingleS := false
	for _, suf := range []string{"sses", "zzes", "xes", "ches", "shes", "tzes"} {
		if strings.HasSuffix(w, suf) && len(w) > len(suf)+1 {
			stem := w[:len(w)-2]
			if len(stem) >= 2 && stem[len(stem)-1] == stem[len(stem)-2] {
				out = append(out, stem, stem[:len(stem)-1])
			} else {
				out = append(out, stem)
			}
			matchedDoubledOrCluster = true
			break
		}
	}
	if !matchedDoubledOrCluster && len(w) > 4 && (strings.HasSuffix(w, "ses") || strings.HasSuffix(w, "zes") || strings.HasSuffix(w, "oes")) {
		// Non-doubled -ses/-zes/-oes mostly come from -se/-ze/-oe + s (loses->lose,
		// floes->floe); only a few -s+es nouns/verbs collide with spurious -e entries
		// in ECDICT (e.g., bus/buse, lens/lense, alias/aliase, refocus/refocuse).
		switch w {
		case "buses", "gases", "lenses", "aliases", "refocuses":
			out = append(out, w[:len(w)-2], w[:len(w)-1])
		default:
			out = append(out, w[:len(w)-1], w[:len(w)-2])
		}
		emittedSingleS = true
	}
	if !emittedSingleS && strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss") && len(w) > 2 {
		out = append(out, w[:len(w)-1])
	}

	// Past tense / past participle (-ed / -d)
	if strings.HasSuffix(w, "ied") && len(w) > 4 {
		out = append(out, w[:len(w)-3]+"y")
	}
	if strings.HasSuffix(w, "ed") && len(w) > 4 {
		stem := w[:len(w)-2]
		if len(stem) >= 2 && stem[len(stem)-1] == stem[len(stem)-2] {
			// Try stem before undoubled (e.g., added -> add before stopped -> stop).
			out = append(out, stem, stem[:len(stem)-1])
		}
		out = append(out, stem+"e", stem)
	}
	if strings.HasSuffix(w, "d") && len(w) > 3 {
		out = append(out, w[:len(w)-1])
	}

	// Gerund / present participle (-ing)
	if strings.HasSuffix(w, "ying") && len(w) > 4 {
		out = append(out, w[:len(w)-4]+"ie")
	}
	if strings.HasSuffix(w, "ing") && len(w) >= 5 {
		stem := w[:len(w)-3]
		if len(stem) >= 3 && stem[len(stem)-1] == stem[len(stem)-2] {
			out = append(out, stem, stem[:len(stem)-1])
		}
		out = append(out, stem+"e")
		if len(stem) >= 3 {
			out = append(out, stem)
		}
	}

	// Adverbs (-ly)
	if strings.HasSuffix(w, "ily") && len(w) > 4 {
		out = append(out, w[:len(w)-3]+"y")
	}
	if strings.HasSuffix(w, "ally") && len(w) > 5 {
		out = append(out, w[:len(w)-4], w[:len(w)-2])
	}
	if strings.HasSuffix(w, "ly") && len(w) > 4 {
		out = append(out, w[:len(w)-1]+"e", w[:len(w)-2]+"e", w[:len(w)-2])
	}

	// Comparatives & superlatives (-er / -est)
	if strings.HasSuffix(w, "ier") && len(w) > 4 {
		out = append(out, w[:len(w)-3]+"y")
	}
	if strings.HasSuffix(w, "er") && len(w) > 4 {
		out = appendErEstCandidates(out, w[:len(w)-2])
	}
	if strings.HasSuffix(w, "iest") && len(w) > 5 {
		out = append(out, w[:len(w)-4]+"y")
	}
	if strings.HasSuffix(w, "est") && len(w) > 5 {
		out = appendErEstCandidates(out, w[:len(w)-3])
	}

	return out
}

func appendErEstCandidates(out []string, stem string) []string {
	if len(stem) >= 2 && stem[len(stem)-1] == stem[len(stem)-2] {
		// FLOSS letters (l, s, f, z) and 3-char stems (odd) double naturally;
		// longer CVC-doubled stems (redder->red, bigger->big) drop the duplicate first.
		if isFlossDouble(stem[len(stem)-1]) || len(stem) < 4 {
			return append(out, stem, stem[:len(stem)-1])
		}
		return append(out, stem[:len(stem)-1], stem)
	}
	if preferSilentEForComparative(stem) {
		return append(out, stem+"e", stem)
	}
	return append(out, stem, stem+"e")
}

func isFlossDouble(b byte) bool {
	return b == 'l' || b == 's' || b == 'f' || b == 'z'
}

func preferSilentEForComparative(stem string) bool {
	n := len(stem)
	if n < 2 {
		return false
	}
	last := stem[n-1]
	prev := stem[n-2]
	if last == 'u' {
		return true
	}
	if last == 'l' && !isASCIIVowel(prev) {
		return true
	}
	for _, suf := range []string{"rg", "rc", "ns", "rs", "ls", "oos", "ang", "ong", "ung"} {
		if strings.HasSuffix(stem, suf) {
			return true
		}
	}
	if last == 'w' || last == 'x' || last == 'y' {
		return false
	}
	// Single vowel + single consonant (CVCe base like wider->wide, nicer->nice),
	// excluding multisyllabic -al/-ed adjectives (local, wicked).
	if !isASCIIVowel(last) && isASCIIVowel(prev) && (n == 2 || !isASCIIVowel(stem[n-3])) {
		if n >= 4 && (strings.HasSuffix(stem, "al") || strings.HasSuffix(stem, "ed")) {
			return false
		}
		return true
	}
	return false
}

func isASCIIVowel(b byte) bool {
	return b == 'a' || b == 'e' || b == 'i' || b == 'o' || b == 'u'
}
