package main

import "testing"

func TestPlasmaPanelLocation(t *testing.T) {
	data := `[Containments][1]
plugin=org.kde.panel
location=4
lastScreen=0
[Containments][23]
lastScreen=1
location=3
plugin=org.kde.panel
[Containments][23][Applets][28]
location=4
plugin=org.kde.plasma.systemtray
`
	edge, screen := plasmaPanelLocation(data)
	if edge != panelTop || screen != 1 {
		t.Fatalf("got edge=%v screen=%d, want top of screen 1", edge, screen)
	}
	edge, screen = plasmaPanelLocation("")
	if edge != panelBottom || screen != 0 {
		t.Fatalf("unexpected fallback: %v %d", edge, screen)
	}
}
