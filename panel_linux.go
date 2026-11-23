package main

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

func panelNeedsBackdrop() bool { return os.Getenv("WAYLAND_DISPLAY") != "" }

var panelLocCache struct {
	mu        sync.Mutex
	checkedAt time.Time
	modTime   time.Time
	path      string
	edge      panelEdge
	screen    int
	valid     bool
}

func desktopPanelLocation() (panelEdge, int) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return panelBottom, 0
	}
	path := filepath.Join(dir, "plasma-org.kde.plasma.desktop-appletsrc")
	now := time.Now()

	panelLocCache.mu.Lock()
	defer panelLocCache.mu.Unlock()

	if panelLocCache.path == path && now.Sub(panelLocCache.checkedAt) < 2*time.Second {
		if panelLocCache.valid {
			return panelLocCache.edge, panelLocCache.screen
		}
		return panelBottom, 0
	}

	panelLocCache.path = path
	panelLocCache.checkedAt = now

	info, err := os.Stat(path)
	if err != nil {
		panelLocCache.valid = false
		panelLocCache.modTime = time.Time{}
		return panelBottom, 0
	}
	if panelLocCache.valid && info.ModTime().Equal(panelLocCache.modTime) {
		return panelLocCache.edge, panelLocCache.screen
	}

	data, err := os.ReadFile(path)
	if err != nil {
		panelLocCache.valid = false
		panelLocCache.modTime = time.Time{}
		return panelBottom, 0
	}
	edge, screen := plasmaPanelLocation(string(data))
	panelLocCache.modTime = info.ModTime()
	panelLocCache.edge = edge
	panelLocCache.screen = screen
	panelLocCache.valid = true
	return edge, screen
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
