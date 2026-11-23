package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestWindowsInProcessSAPISpVoiceAndZeroPowerShellContract(t *testing.T) {
	fset := token.NewFileSet()
	rawWin, err := os.ReadFile("speech_windows.go")
	if err != nil {
		t.Fatalf("read speech_windows.go: %v", err)
	}
	winSrc := string(rawWin)
	winFile, err := parser.ParseFile(fset, "speech_windows.go", rawWin, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse speech_windows.go: %v", err)
	}
	for _, imp := range winFile.Imports {
		if imp.Path != nil && imp.Path.Value == `"C"` {
			t.Fatal("speech_windows.go must remain pure Go (zero CGO)")
		}
	}
	for _, sym := range []string{"SAPI.SpVoice", "SVSFlagsAsync", "SVSFPurgeBeforeSpeak", "CLSIDFromProgID", "CoCreateInstance"} {
		if !strings.Contains(winSrc, sym) {
			t.Fatalf("speech_windows.go missing COM SAPI symbol %q", sym)
		}
	}

	// Ensure no production speech file spawns powershell.exe or System.Speech.
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range files {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, p, raw, parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			unq, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			low := strings.ToLower(strings.TrimSpace(unq))
			if strings.Contains(low, "powershell.exe") || strings.Contains(low, "system.speech") {
				t.Fatalf("production file %s still references legacy %q", p, unq)
			}
			return true
		})
	}
}
