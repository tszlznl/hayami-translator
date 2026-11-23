//go:build !windows && !linux

package main

import "github.com/egoist/mygo"

func migrateLegacyStartup() {}

func setPlatformOpenAtLogin(enabled bool) error {
	return mygo.App.SetOpenAtLogin(enabled)
}
