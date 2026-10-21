package main

import (
	"context"
	"os"
	"strings"
	"testing"
)

// TestLiveAzureTranslator and TestLiveBaiduTranslator are opt-in checks
// against the real services. Credentials are read from the environment, never
// from the repository.
func TestLiveAzureTranslator(t *testing.T) {
	if os.Getenv("NEXTT_LIVE") == "" {
		t.Skip("set NEXTT_LIVE=1 to call the live services")
	}
	key := os.Getenv("AZURE_TRANSLATOR_KEY")
	endpoint := os.Getenv("AZURE_TRANSLATOR_ENDPOINT")
	if key == "" || endpoint == "" {
		t.Skip("AZURE_TRANSLATOR_KEY and AZURE_TRANSLATOR_ENDPOINT are required")
	}
	cfg := defaultConfig()
	p := Provider{
		ID:       "azure",
		Name:     "Azure 翻译",
		Kind:     providerAzure,
		Enabled:  true,
		Endpoint: endpoint,
		Region:   strings.TrimSpace(os.Getenv("AZURE_TRANSLATOR_REGION")),
	}
	var out strings.Builder
	if err := translateWithProvider(context.Background(), cfg, p, key, nil, "Hello", func(s string) {
		out.WriteString(s)
	}); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) == "" {
		t.Fatal("empty translation")
	}
	t.Logf("Azure result=%q", out.String())
}

func TestLiveBaiduTranslator(t *testing.T) {
	if os.Getenv("NEXTT_LIVE") == "" {
		t.Skip("set NEXTT_LIVE=1 to call the live services")
	}
	appID := os.Getenv("BAIDU_TRANSLATE_APP_ID")
	key := os.Getenv("BAIDU_TRANSLATE_KEY")
	endpoint := os.Getenv("BAIDU_TRANSLATE_ENDPOINT")
	if appID == "" || key == "" || endpoint == "" {
		t.Skip("BAIDU_TRANSLATE_APP_ID, BAIDU_TRANSLATE_KEY and BAIDU_TRANSLATE_ENDPOINT are required")
	}
	cfg := defaultConfig()
	p := Provider{
		ID:       "baidu",
		Name:     "百度翻译",
		Kind:     providerBaidu,
		Enabled:  true,
		Endpoint: endpoint,
		AppID:    appID,
	}
	var out strings.Builder
	if err := translateWithProvider(context.Background(), cfg, p, key, nil, "Hello", func(s string) {
		out.WriteString(s)
	}); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) == "" {
		t.Fatal("empty translation")
	}
	t.Logf("Baidu result=%q", out.String())
}
