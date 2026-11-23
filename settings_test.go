package main

import "testing"

func TestSettingsDirty(t *testing.T) {
	cfg := defaultConfig()
	a := &app{
		cfg:            cfg,
		toolbarEnabled: !cfg.DisableSelectionToolbar,
		settingsState: settingsState{
			form: cfg.Clone(),
		},
	}
	if a.settingsDirty() {
		t.Fatal("a fresh draft should match the running config")
	}

	a.form.Hotkey = "Alt+Shift+X"
	if !a.settingsDirty() {
		t.Fatal("editing a field should mark the draft dirty")
	}

	a.form = cfg.Clone()
	if a.settingsDirty() {
		t.Fatal("restoring the draft should clear the dirty state")
	}

	a.toolbarEnabled = cfg.DisableSelectionToolbar
	if !a.settingsDirty() {
		t.Fatal("changing the selection toolbar should mark the draft dirty")
	}

	a.toolbarEnabled = !cfg.DisableSelectionToolbar
	a.form.MultiEngine = !cfg.MultiEngine
	if !a.settingsDirty() {
		t.Fatal("changing MultiEngine should mark the draft dirty")
	}
}

func TestHasAnyResult(t *testing.T) {
	a := &app{}
	if a.hasAnyResult() {
		t.Fatal("empty app should have no result")
	}
	a.result = "some translation"
	if !a.hasAnyResult() {
		t.Fatal("single engine result should be recognized")
	}

	a.multiEngine = true
	a.results = []providerResult{
		{ID: "p1", Result: ""},
		{ID: "p2", Result: "translated by p2"},
	}
	if !a.hasAnyResult() {
		t.Fatal("multi engine with at least one non-empty result should return true")
	}

	a.results[1].Result = ""
	if a.hasAnyResult() {
		t.Fatal("multi engine with all empty results should return false")
	}
}

func TestNormalizeProvidersKeyTrim(t *testing.T) {
	c := Config{
		Providers: []Provider{
			{ID: "p1", Kind: providerOpenAI, Key: "  sk-secret-key  \n\t "},
		},
	}
	c.NormalizeProviders()
	if c.Providers[0].Key != "sk-secret-key" {
		t.Fatalf("expected key to be trimmed, got %q", c.Providers[0].Key)
	}
}

func TestToolbarToggleSyncFormDirty(t *testing.T) {
	cfg := defaultConfig()
	a := &app{
		cfg:            cfg,
		toolbarEnabled: !cfg.DisableSelectionToolbar,
		settingsState: settingsState{
			form: cfg.Clone(),
		},
	}
	if a.settingsDirty() {
		t.Fatal("initially clean")
	}

	// Simulating tray toggle
	toggled := !a.toolbarEnabled
	a.cfg.DisableSelectionToolbar = !toggled
	a.toolbarEnabled = toggled
	a.form.DisableSelectionToolbar = !toggled

	if a.settingsDirty() {
		t.Fatal("settings should not be dirty when tray synchronized form and cfg")
	}
}

func TestSettingsDirtyWithEmptyProviders(t *testing.T) {
	cfg := Config{
		ProvidersConfigured: true,
		Providers:           []Provider{}, // non-nil empty slice
	}
	cfg.FillDefaults()
	a := &app{
		cfg:            cfg,
		toolbarEnabled: !cfg.DisableSelectionToolbar,
		settingsState: settingsState{
			form: cfg.Clone(),
		},
	}
	if a.settingsDirty() {
		t.Fatal("empty providers list should not cause settings to be marked dirty")
	}

	// Also test when one is nil and the other is empty slice
	a.form.Providers = nil
	if a.settingsDirty() {
		t.Fatal("nil providers and empty slice providers should both be considered clean")
	}
}
