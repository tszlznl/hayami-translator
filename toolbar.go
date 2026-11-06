package main

import "sync/atomic"

var selectionToolbarEnabled atomic.Bool

func setSelectionToolbarEnabled(enabled bool) {
	selectionToolbarEnabled.Store(enabled)
}
