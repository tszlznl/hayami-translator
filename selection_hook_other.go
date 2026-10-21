//go:build !windows

package main

// The drag-selection toolbar is Windows-only; selection translation still
// works through the global shortcut where the platform supports it.
func startSelectionHook(*app) error { return nil }

func stopSelectionHook() {}
