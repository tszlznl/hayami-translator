package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLITranslateAndDoctor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"object":"list","data":[{"id":"gpt-4o-mini"}]}`)
		case "/v1/chat/completions":
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"你好，CLI\"}}]}\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("APPDATA", xdg)

	appDir := filepath.Join(xdg, "Hayami Translator")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := defaultConfig()
	cfg.BaseURL = srv.URL + "/v1"
	cfg.Providers = []Provider{
		{
			ID:      "openai",
			Name:    "LocalMock",
			Kind:    providerOpenAI,
			Enabled: true,
			BaseURL: srv.URL + "/v1",
			Model:   "gpt-4o-mini",
			Vision:  true,
		},
	}
	cfgBytes, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "config.json"), cfgBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	// 1. Plain stdout translate.
	var stdout, stderr bytes.Buffer
	code := runCLITranslate(nil, strings.NewReader("hello cli\n"), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("runCLITranslate exit=%d stderr=%q", code, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "你好，CLI" {
		t.Fatalf("stdout = %q, want 你好，CLI", stdout.String())
	}

	// 2. JSON translate with --target and --mode.
	stdout.Reset()
	stderr.Reset()
	code = runCLITranslate([]string{"--target", "简体中文", "--mode", "polish", "--json"}, strings.NewReader("draft"), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("runCLITranslate --json exit=%d stderr=%q", code, stderr.String())
	}
	var obj map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &obj); err != nil {
		t.Fatalf("unmarshal translate --json: %v (%q)", err, stdout.String())
	}
	if obj["result"] != "你好，CLI" || obj["mode"] != "polish" {
		t.Fatalf("unexpected translate JSON: %+v", obj)
	}

	// 3. Empty stdin fails non-zero.
	stdout.Reset()
	stderr.Reset()
	if code := runCLITranslate(nil, strings.NewReader("   "), &stdout, &stderr); code == 0 {
		t.Fatal("expected non-zero exit code on empty stdin")
	}

	// 4. Doctor healthy report (both text and --json).
	stdout.Reset()
	stderr.Reset()
	if code := runCLIDoctor(nil, &stdout, &stderr); code != 0 {
		t.Fatalf("runCLIDoctor exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := runCLIDoctor([]string{"--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("runCLIDoctor --json exit=%d stderr=%q", code, stderr.String())
	}
	var docRep doctorReport
	if err := json.Unmarshal(stdout.Bytes(), &docRep); err != nil || !docRep.OK {
		t.Fatalf("unexpected doctor JSON report: err=%v rep=%+v", err, docRep)
	}
}
