package main

import (
	"testing"

	"hayami/internal/config"
)

func TestNewModeHasUniqueID(t *testing.T) {
	cfg := defaultConfig()
	cfg.Modes = []config.Mode{
		{ID: "custom", Name: "A", Prompt: "p", Kind: config.ModeMaterial},
		{ID: "custom-2", Name: "B", Prompt: "p", Kind: config.ModeMaterial},
	}
	m := newMode(cfg)
	if m.ID != "custom-3" {
		t.Fatalf("id = %q, want custom-3", m.ID)
	}
	if m.Kind != config.ModeMaterial || m.Name == "" || m.Prompt == "" {
		t.Fatalf("mode = %#v", m)
	}
}

func TestContainsMode(t *testing.T) {
	modes := []config.Mode{{ID: "a"}, {ID: "b"}}
	if !containsMode(modes, "b") || containsMode(modes, "c") {
		t.Fatal("containsMode is wrong")
	}
}

func TestApplyThemeMode(t *testing.T) {
	defer applyThemeMode(config.ThemeSystem)
	for _, tc := range []struct{ in, want string }{
		{config.ThemeDark, config.ThemeDark},
		{config.ThemeLight, config.ThemeLight},
		{config.ThemeSystem, config.ThemeSystem},
		{"bogus", config.ThemeSystem},
	} {
		applyThemeMode(tc.in)
		if themeMode != tc.want {
			t.Errorf("applyThemeMode(%q) = %q, want %q", tc.in, themeMode, tc.want)
		}
	}
}
