package main

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func panelNeedsBackdrop() bool { return os.Getenv("WAYLAND_DISPLAY") != "" }

func desktopPanelLocation() (panelEdge, int) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return panelBottom, 0
	}
	data, err := os.ReadFile(filepath.Join(dir, "plasma-org.kde.plasma.desktop-appletsrc"))
	if err != nil {
		return panelBottom, 0
	}
	return plasmaPanelLocation(string(data))
}

func plasmaPanelLocation(data string) (panelEdge, int) {
	type panel struct {
		edge             panelEdge
		screen           int
		isPanel, hasTray bool
	}
	panels := map[string]*panel{}
	var order, section []string
	scanner := bufio.NewScanner(strings.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "[") {
			section = strings.Split(strings.Trim(line, "[]"), "][")
			continue
		}
		if len(section) < 2 || section[0] != "Containments" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		id := section[1]
		p := panels[id]
		if p == nil {
			p = &panel{}
			panels[id] = p
			order = append(order, id)
		}
		if len(section) == 4 && section[2] == "Applets" && key == "plugin" && value == "org.kde.plasma.systemtray" {
			p.hasTray = true
		}
		if len(section) != 2 {
			continue
		}
		switch key {
		case "plugin":
			p.isPanel = value == "org.kde.panel"
		case "lastScreen":
			p.screen, _ = strconv.Atoi(value)
		case "location":
			switch value {
			case "3":
				p.edge = panelTop
			case "5":
				p.edge = panelLeft
			case "6":
				p.edge = panelRight
			default:
				p.edge = panelBottom
			}
		}
	}
	for _, id := range order {
		if p := panels[id]; p.isPanel && p.hasTray {
			return p.edge, p.screen
		}
	}
	return panelBottom, 0
}
