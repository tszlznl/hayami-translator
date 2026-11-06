package main

import "testing"

func TestEnabledProviders(t *testing.T) {
	cfg := Config{Providers: []Provider{
		{ID: "off", Kind: providerOpenAI, Enabled: false, BaseURL: "x", Model: "m", Vision: true},
		{ID: "azure", Kind: providerAzure, Enabled: true},
		{ID: "novision", Kind: providerOpenAI, Enabled: true, BaseURL: "x", Model: "m", Vision: false},
		{ID: "vision", Kind: providerOpenAI, Enabled: true, BaseURL: "x", Model: "m", Vision: true},
		{ID: "baidu", Kind: providerBaidu, Enabled: true},
	}}
	text := cfg.EnabledProviders(false)
	if len(text) != 4 || text[0].ID != "azure" || text[3].ID != "baidu" {
		t.Fatalf("text providers %#v", text)
	}
	image := cfg.EnabledProviders(true)
	if len(image) != 1 || image[0].ID != "vision" {
		t.Fatalf("image providers %#v", image)
	}
	if !providerConfigured(Provider{Kind: providerAzure}, "k") {
		t.Error("Azure with a key should be configured")
	}
	if providerConfigured(Provider{Kind: providerBaidu, AppID: "a"}, "") {
		t.Error("Baidu without a secret should not be configured")
	}
}

func TestDefaultConfigHasOpenAIProvider(t *testing.T) {
	cfg := defaultConfig()
	if !cfg.ProvidersConfigured || len(cfg.Providers) != 1 {
		t.Fatalf("providers %#v configured %v", cfg.Providers, cfg.ProvidersConfigured)
	}
	p := cfg.Providers[0]
	if p.Kind != providerOpenAI || p.ID != "openai" || !p.Enabled || !p.Vision {
		t.Fatalf("provider %#v", p)
	}
	clone := cfg.Clone()
	clone.Providers[0].Model = "changed"
	if cfg.Providers[0].Model == "changed" {
		t.Error("clone shares the provider slice")
	}
}
