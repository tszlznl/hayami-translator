package main

import "strings"

type wordInfo struct {
	Word     string
	Phonetic string
	Pos      string
	Def      string
}

// Built-in dictionary for high frequency words & demo showcase
var builtInDict = map[string]wordInfo{
	"quiet":      {Word: "quiet", Phonetic: "/ˈkwaɪ.ət/", Pos: "adj.", Def: "安静的；平静的"},
	"design":     {Word: "design", Phonetic: "/dɪˈzaɪn/", Pos: "n./v.", Def: "设计；构想"},
	"tool":       {Word: "tool", Phonetic: "/tuːl/", Pos: "n.", Def: "工具；用具"},
	"trust":      {Word: "trust", Phonetic: "/trʌst/", Pos: "n./v.", Def: "信任；信赖"},
	"reader":     {Word: "reader", Phonetic: "/ˈriːdər/", Pos: "n.", Def: "读者；阅读器"},
	"interface":  {Word: "interface", Phonetic: "/ˈɪntərfeɪs/", Pos: "n.", Def: "界面；接口"},
	"pixel":      {Word: "pixel", Phonetic: "/ˈpɪksl/", Pos: "n.", Def: "像素"},
	"simple":     {Word: "simple", Phonetic: "/ˈsɪmpl/", Pos: "adj.", Def: "简单的；朴素的"},
	"fluent":     {Word: "fluent", Phonetic: "/ˈfluːənt/", Pos: "adj.", Def: "流畅的；流利的"},
	"native":     {Word: "native", Phonetic: "/ˈneɪtɪv/", Pos: "adj.", Def: "本机的；原生的"},
	"translator": {Word: "translator", Phonetic: "/trænzˈleɪtər/", Pos: "n.", Def: "翻译工具；译者"},
	"window":     {Word: "window", Phonetic: "/ˈwɪndoʊ/", Pos: "n.", Def: "窗口；窗户"},
	"word":       {Word: "word", Phonetic: "/wɜːrd/", Pos: "n.", Def: "单词；话语"},
	"fast":       {Word: "fast", Phonetic: "/fæst/", Pos: "adj./adv.", Def: "快速的；紧固的"},
	"clean":      {Word: "clean", Phonetic: "/kliːn/", Pos: "adj.", Def: "干净的；简洁的"},
	"engine":     {Word: "engine", Phonetic: "/ˈendʒɪn/", Pos: "n.", Def: "引擎；发动机"},
	"setting":    {Word: "setting", Phonetic: "/ˈsetɪŋ/", Pos: "n.", Def: "设置；环境"},
	"history":    {Word: "history", Phonetic: "/ˈhɪstri/", Pos: "n.", Def: "历史；历史记录"},
	"screen":     {Word: "screen", Phonetic: "/skriːn/", Pos: "n.", Def: "屏幕；屏风"},
	"shortcut":   {Word: "shortcut", Phonetic: "/ˈʃɔːrtkʌt/", Pos: "n.", Def: "快捷键；捷径"},
}

// lookupWord returns the dictionary entry for a single word. found reports
// whether the built-in dictionary contains the word; when it does not, the
// caller can resolve the word with an online translation instead.
func lookupWord(s string) (info wordInfo, found bool) {
	cleaned := strings.ToLower(strings.TrimSpace(s))
	cleaned = strings.Trim(cleaned, ".,!?\"'()[]{}")
	info, found = builtInDict[cleaned]
	if !found {
		info = wordInfo{Word: s, Phonetic: "/en/", Pos: "释义"}
	}
	return info, found
}
