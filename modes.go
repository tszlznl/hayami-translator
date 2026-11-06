package main

import (
	"fmt"
	"log"

	"hayami/internal/config"
)

func containsMode(modes []config.Mode, id string) bool {
	for _, m := range modes {
		if m.ID == id {
			return true
		}
	}
	return false
}

// newMode appends a valid custom mode with a unique id.
func newMode(cfg Config) config.Mode {
	exists := func(id string) bool {
		return containsMode(cfg.AllModes(), id)
	}
	id := "custom"
	for n := 2; exists(id); n++ {
		id = fmt.Sprintf("custom-%d", n)
	}
	return config.Mode{
		ID:     id,
		Name:   "自定义",
		Prompt: "用{{target}}处理用户提供的文字。只输出结果。",
		Kind:   config.ModeMaterial,
	}
}

func (a *app) currentMode() config.Mode {
	if m, ok := a.cfg.Mode(a.activeMode); ok {
		return m
	}
	modes := a.cfg.AllModes()
	return modes[0]
}

func (a *app) modeName() string {
	return a.currentMode().Name
}

// cycleMode moves to the next prompt preset and persists the choice. An
// existing result is regenerated so the switch is immediately visible.
func (a *app) cycleMode() {
	modes := a.cfg.AllModes()
	if len(modes) == 0 {
		return
	}
	index := 0
	for i, m := range modes {
		if m.ID == a.activeMode {
			index = i
			break
		}
	}
	a.activeMode = modes[(index+1)%len(modes)].ID
	a.cfg.ActiveMode = a.activeMode
	a.form.ActiveMode = a.activeMode
	if err := a.cfg.Save(); err != nil {
		log.Print("saving active mode: ", err)
		a.status = "模式已切换，但保存失败：" + err.Error()
	} else {
		a.status = "模式：" + a.modeName()
	}
	if a.hasAnyResult() && !a.streaming {
		a.startTranslation()
	}
}
