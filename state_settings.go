package main

import (
	"context"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

type providerProbeState struct {
	testing bool
	ok      bool
	text    string
	hint    string
	reqID   int
	cancel  context.CancelFunc
}

type settingsState struct {
	form                  Config
	formTempText          string
	formMaxTokensText     string
	formModelParamsSynced bool
	settingsStatus        string
	settingsCloseAsk      bool
	providerDeleteArmed   string
	providerProbes        map[string]providerProbeState
	settingsTab           int
	settingsModesEditing  bool
	settingsScroll        ui.ScrollState
}

func (a *app) cancelAllProviderProbes() {
	for id, st := range a.providerProbes {
		if st.cancel != nil {
			st.cancel()
		}
		delete(a.providerProbes, id)
	}
}

func (a *app) runProviderProbe(p Provider) {
	if a.providerProbes == nil {
		a.providerProbes = make(map[string]providerProbeState)
	}
	prev := a.providerProbes[p.ID]
	if prev.cancel != nil {
		prev.cancel()
	}

	key := strings.TrimSpace(p.Key)
	if key == "" {
		for _, saved := range a.cfg.Providers {
			if saved.ID == p.ID && strings.TrimSpace(saved.Key) != "" {
				key = strings.TrimSpace(saved.Key)
				break
			}
		}
	}

	// Fast synchronous local validation when credentials or required fields are missing:
	// avoids spawning background HTTP requests when obvious credentials are absent.
	switch p.Kind {
	case providerOpenAI:
		if strings.TrimSpace(p.BaseURL) == "" || (key == "" && !isLoopbackURL(p.BaseURL)) || strings.TrimSpace(p.Model) == "" {
			rep := probeProvider(context.Background(), a.form, p, key)
			a.providerProbes[p.ID] = providerProbeState{
				ok:    rep.OK,
				text:  rep.ErrTitle,
				hint:  rep.ErrHint,
				reqID: prev.reqID + 1,
			}
			return
		}
	case providerAzure:
		if key == "" {
			rep := probeProvider(context.Background(), a.form, p, key)
			a.providerProbes[p.ID] = providerProbeState{
				ok:    rep.OK,
				text:  rep.ErrTitle,
				hint:  rep.ErrHint,
				reqID: prev.reqID + 1,
			}
			return
		}
	case providerBaidu:
		if strings.TrimSpace(p.AppID) == "" || key == "" {
			rep := probeProvider(context.Background(), a.form, p, key)
			a.providerProbes[p.ID] = providerProbeState{
				ok:    rep.OK,
				text:  rep.ErrTitle,
				hint:  rep.ErrHint,
				reqID: prev.reqID + 1,
			}
			return
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	reqID := prev.reqID + 1
	a.providerProbes[p.ID] = providerProbeState{
		testing: true,
		text:    "正在测试连接…",
		reqID:   reqID,
		cancel:  cancel,
	}
	cfgSnap := a.form.Clone()
	provSnap := p

	go func() {
		defer cancel()
		rep := probeProvider(ctx, cfgSnap, provSnap, key)
		wasCanceled := ctx.Err() == context.Canceled
		if wasCanceled {
			return
		}
		a.postUI(func() {
			if a.quitting || a.providerProbes == nil {
				return
			}
			cur, ok := a.providerProbes[provSnap.ID]
			if !ok || cur.reqID != reqID {
				return
			}
			st := providerProbeState{
				testing: false,
				ok:      rep.OK,
				reqID:   reqID,
			}
			if rep.OK {
				st.text = rep.Summary
			} else {
				st.text = rep.ErrTitle
				st.hint = rep.ErrHint
			}
			a.providerProbes[provSnap.ID] = st
			a.refreshSettingsWindow()
		})
	}()
}

func (s *settingsState) syncFormModelParams() {
	s.formTempText = strconv.FormatFloat(s.form.Temperature, 'f', -1, 64)
	s.formMaxTokensText = strconv.Itoa(s.form.MaxTokens)
	s.formModelParamsSynced = true
}

func (a *app) saveSettings() {
	f := a.form.Clone()
	if a.formModelParamsSynced || strings.TrimSpace(a.formTempText) != "" {
		raw := strings.TrimSpace(a.formTempText)
		if raw == "" {
			a.settingsStatus = "请填写采样温度（0 到 2 之间的数字）"
			return
		}
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			a.settingsStatus = "采样温度格式无效，请输入 0 到 2 之间的数字"
			return
		}
		f.Temperature = v
	}
	if a.formModelParamsSynced || strings.TrimSpace(a.formMaxTokensText) != "" {
		raw := strings.TrimSpace(a.formMaxTokensText)
		if raw == "" {
			a.settingsStatus = "请填写最大输出 Token（1 到 200000 之间的整数）"
			return
		}
		v, err := strconv.Atoi(raw)
		if err != nil {
			a.settingsStatus = "最大输出 Token 格式无效，请输入 1 到 200000 之间的整数"
			return
		}
		f.MaxTokens = v
	}
	f.FillDefaults()
	f.NormalizeProviders()
	f.BaseURL = strings.TrimSpace(f.BaseURL)
	f.Model = strings.TrimSpace(f.Model)
	f.Hotkey = strings.TrimSpace(f.Hotkey)
	f.ScreenshotHotkey = strings.TrimSpace(f.ScreenshotHotkey)
	f.InputHotkey = strings.TrimSpace(f.InputHotkey)
	if err := validateConfig(f); err != nil {
		a.settingsStatus = err.Error()
		return
	}
	// Save keys first; if keyring fails, abort without overwriting config file
	if err := saveProviderKeys(f); err != nil {
		a.settingsStatus = "保存失败，密钥写入凭据管理器失败：" + err.Error()
		return
	}
	if err := f.Save(); err != nil {
		a.settingsStatus = "保存配置文件失败：" + err.Error()
		return
	}
	for _, old := range a.cfg.Providers {
		kept := false
		for _, p := range f.Providers {
			if p.ID == old.ID {
				kept = true
				break
			}
		}
		if !kept {
			deleteProviderKey(old)
		}
	}
	a.cfg = f
	setSpeechConfig(f)
	if a.store != nil && f.HistoryLimit > 0 {
		_ = a.store.SetMaxEntries(f.HistoryLimit)
	}
	a.form = f.Clone()
	a.syncFormModelParams()
	a.toolbarEnabled = !f.DisableSelectionToolbar
	applyThemeMode(f.Theme)
	a.refreshTheme()
	a.setPanelPinned(f.PopupPinned)
	a.providerDeleteArmed = ""
	a.providerName = activeProviderLabel(f)
	a.targetLang = f.TargetLang
	a.activeMode = f.ActiveMode
	a.multiEngine = f.MultiEngine
	setSelectionToolbarEnabled(a.toolbarEnabled)
	a.registerHotkeys()
	a.refreshTrayMenu()
	if !hasConfiguredProvider(f) {
		a.settingsStatus = "已保存，但没有可用的翻译服务"
	} else if a.hotkeyErr != "" {
		a.settingsStatus = "已保存。" + a.hotkeyErr
	} else {
		a.settingsStatus = "已保存"
	}
}
