//go:build !windows && !linux

package main

// Automatic selection monitoring is not supported on this platform.
func startSelectionHook(*app) error { return nil }

func stopSelectionHook() {}
