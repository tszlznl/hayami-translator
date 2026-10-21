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
}
