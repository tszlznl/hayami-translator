package main

import "testing"

func TestSettingsDirty(t *testing.T) {
	cfg := defaultConfig()
	a := &app{
		cfg:            cfg,
		form:           cfg.clone(),
		toolbarEnabled: !cfg.DisableSelectionToolbar,
	}
	if a.settingsDirty() {
		t.Fatal("a fresh draft should match the running config")
	}

	a.form.Hotkey = "Alt+Shift+X"
	if !a.settingsDirty() {
		t.Fatal("editing a field should mark the draft dirty")
	}

	a.form = cfg.clone()
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
