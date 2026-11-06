//go:build !linux

package main

func panelNeedsBackdrop() bool { return false }

func desktopPanelLocation() (panelEdge, int) {
	return panelBottom, -1
}
