package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStreamChat(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Hello \"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"world\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	cfg := defaultConfig()
	cfg.BaseURL = srv.URL + "/v1"
	var out strings.Builder
	err := streamChat(context.Background(), cfg, "secret", textMessages(cfg, "hi"), func(s string) {
		out.WriteString(s)
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.String() != "Hello world" {
		t.Errorf("streamed %q", out.String())
	}
	if gotPath != "/v1/chat/completions" {
		t.Errorf("path %q", gotPath)
	}
	if gotAuth != "Bearer secret" {
		t.Errorf("authorization %q", gotAuth)
	}
}

func TestStreamChatErrorBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"message":"bad key"}}`)
	}))
	defer srv.Close()

	cfg := defaultConfig()
	cfg.BaseURL = srv.URL
	err := streamChat(context.Background(), cfg, "k", textMessages(cfg, "hi"), func(string) {})
	if err == nil || !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "bad key") {
		t.Fatalf("error %v", err)
	}
}

func TestStreamChatNonStreamingReply(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"one shot"}}]}`)
	}))
	defer srv.Close()

	cfg := defaultConfig()
	cfg.BaseURL = srv.URL
	var out strings.Builder
	if err := streamChat(context.Background(), cfg, "k", textMessages(cfg, "hi"), func(s string) { out.WriteString(s) }); err != nil {
		t.Fatal(err)
	}
	if out.String() != "one shot" {
		t.Errorf("got %q", out.String())
	}
}

func TestImageMessages(t *testing.T) {
	cfg := defaultConfig()
	cfg.TargetLang = "日本語"
	msgs := imageMessages(cfg, []byte("png"))
	if len(msgs) != 2 {
		t.Fatalf("messages %d", len(msgs))
	}
	parts, ok := msgs[1].Content.([]any)
	if !ok || len(parts) != 2 {
		t.Fatalf("content %#v", msgs[1].Content)
	}
	img, ok := parts[1].(contentPart)
	if !ok || img.ImageURL == nil || !strings.HasPrefix(img.ImageURL.URL, "data:image/png;base64,") {
		t.Fatalf("image part %#v", parts[1])
	}
	text, ok := parts[0].(contentPart)
	if !ok || !strings.Contains(text.Text, "日本語") {
		t.Fatalf("instruction %#v", parts[0])
	}
}

func TestEmptyAPIKey(t *testing.T) {
	cfg := defaultConfig()
	err := streamChat(context.Background(), cfg, " ", textMessages(cfg, "hi"), func(string) {})
	if err == nil || !strings.Contains(err.Error(), "API Key") {
		t.Fatalf("error %v", err)
	}
}
