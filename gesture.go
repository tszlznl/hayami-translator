package main

import "time"

// isDoubleClick reports whether a click that ended dx/dy away from the
// previous one, elapsed later, is the second click of a double-click. The
// 4px tolerance keeps a shaky hand from turning a double-click into a drag.
func isDoubleClick(dx, dy int32, elapsed, window time.Duration, hasPrevious bool) bool {
	if !hasPrevious || elapsed < 0 || elapsed > window {
		return false
	}
	return abs32(dx) <= 4 && abs32(dy) <= 4
}

func abs32(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}
