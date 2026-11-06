package main

import (
	"testing"
	"time"
)

func TestIsDoubleClick(t *testing.T) {
	const window = 500 * time.Millisecond
	for _, tc := range []struct {
		name        string
		dx, dy      int32
		elapsed     time.Duration
		hasPrevious bool
		want        bool
	}{
		{"second click near the first", 2, -2, 120 * time.Millisecond, true, true},
		{"second click inside the window", 0, 0, window, true, true},
		{"too far apart", 8, 0, 100 * time.Millisecond, true, false},
		{"too slow", 0, 0, window + time.Millisecond, true, false},
		{"no previous click", 0, 0, 100 * time.Millisecond, false, false},
		{"clock went backwards", 0, 0, -time.Second, true, false},
	} {
		if got := isDoubleClick(tc.dx, tc.dy, tc.elapsed, window, tc.hasPrevious); got != tc.want {
			t.Errorf("%s: isDoubleClick() = %v, want %v", tc.name, got, tc.want)
		}
	}
}
