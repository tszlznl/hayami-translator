package lang

import (
	"strings"
	"testing"
)

func TestIsSingleWordValid(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"hello", "hello"},
		{"Hello", "hello"},
		{"HELLO", "hello"},
		{"  serendipity  \n", "serendipity"},
		{"(well-known).", "well-known"},
		{"\"ephemeral,\"", "ephemeral"},
		{"“algorithm!”", "algorithm"},
		{"don't", "don't"},
		{"don’t", "don't"},
		{"O'Brien", "o'brien"},
		{"up-to-date", "up-to-date"},
		{"state-of-the-art", "state-of-the-art"},
		{"Well-Known", "well-known"},
		{"well\u2010known", "well-known"},
		{"rock'n'roll", "rock'n'roll"},
		{"a", "a"},
		{"I", "i"},
	}

	for _, tc := range tests {
		got, ok := IsSingleWord(tc.in)
		if !ok || got != tc.want {
			t.Errorf("IsSingleWord(%q) = (%q, %v), want (%q, true)", tc.in, got, ok, tc.want)
		}
	}
}

func TestIsSingleWordRejected(t *testing.T) {
	rejected := []string{
		"",
		"   ",
		"...",
		"---",
		"()",
		"hello world",
		"how are you?",
		"line1\nline2",
		"https://example.com",
		"example.com/foo",
		"user@example.com",
		"/usr/local/bin",
		"--help",
		"-v",
		"main.go",
		"snake_case",
		"SCREAMING_SNAKE",
		"camelCase",
		"getElementById",
		"PascalCase",
		"IsSingleWord",
		"XMLParser",
		"myGo",
		"iPhone",
		"foo2bar",
		"v1.2.3",
		"12345",
		"3.14",
		"-42",
		"-hello",
		"hello-",
		"foo--bar",
		"a-b-c-d-e",
		"foo''bar",
		"foo-'bar",
		"word—another",
		"你好",
		"今天天气真好",
		"こんにちは",
		"안녕하세요",
		"Привет",
		"café",
		"😀",
		strings.Repeat("a", 65),
	}

	for _, in := range rejected {
		if got, ok := IsSingleWord(in); ok {
			t.Errorf("IsSingleWord(%q) = (%q, true), want (\"\", false)", in, got)
		}
	}
}
