//go:build windows

package main

import (
	"log"

	"github.com/egoist/mygo"
	"golang.org/x/sys/windows/registry"
)

const (
	runKeyPath      = `Software\Microsoft\Windows\CurrentVersion\Run`
	approvedKeyPath = `Software\Microsoft\Windows\CurrentVersion\Explorer\StartupApproved\Run`
)

// migrateLegacyStartup moves the old app's login item to the new identifier
// and executable. Task Manager disables a Run entry by writing an odd first
// byte to StartupApproved, so an intentionally disabled item stays off.
func migrateLegacyStartup() {
	run, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return
	}
	defer run.Close()
	if _, _, err := run.GetStringValue(legacyAppIdentifier); err != nil {
		return
	}

	enabled := true
	approved, approvedErr := registry.OpenKey(registry.CURRENT_USER, approvedKeyPath, registry.QUERY_VALUE|registry.SET_VALUE)
	if approvedErr == nil {
		defer approved.Close()
		if state, _, err := approved.GetBinaryValue(legacyAppIdentifier); err == nil && len(state) > 0 {
			enabled = state[0]&1 == 0
		}
	}

	if enabled && !mygo.App.OpenAtLogin() {
		if err := mygo.App.SetOpenAtLogin(true); err != nil {
			log.Print("migrating startup entry: ", err)
			return
		}
	}
	if err := run.DeleteValue(legacyAppIdentifier); err != nil {
		log.Print("removing old startup entry: ", err)
	}
	if approvedErr == nil {
		_ = approved.DeleteValue(legacyAppIdentifier)
	}
}
