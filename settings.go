package main

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"hayami/internal/config"
)

// settingsDirty reports whether the draft differs from the running config.
func (a *app) settingsDirty() bool {
	return !a.form.SameConfig(a.cfg) ||
		a.toolbarEnabled != !a.cfg.DisableSelectionToolbar
}

func (a *app) addFormProvider(kind string) {
	a.providerDeleteArmed = ""
	a.settingsStatus = "已添加新服务，保存后生效"
	id := nextProviderID(a.form, kind)
	p := Provider{
		ID:      id,
		Name:    defaultProviderName(kind),
		Kind:    kind,
		Enabled: true,
		Vision:  kind == providerOpenAI,
	}
	switch kind {
	case providerOpenAI:
		p.BaseURL = "https://api.openai.com/v1"
		p.Model = "gpt-4o-mini"
	case providerAzure:
		p.Endpoint = defaultAzureEndpoint
	case providerBaidu:
		p.Endpoint = defaultBaiduEndpoint
	}
	a.form.Providers = append(a.form.Providers, p)
}

// settingRow renders a settings row: the name and description on the left,
// the control on the right. Rows in a group are separated by hairlines.
func settingRow(c *ui.Context, title, desc string, control func()) {
	t := c.Theme()
	pal := win11Theme(t)

	ui.Row(c).Padding(10, 14).AlignItems(ui.Center).Gap(16).Children(func() {
		ui.Column(c).Grow(1).Shrink(1).Gap(2).Justify(ui.Center).Children(func() {
			ui.Text(c, title).FontSize(13).TextColor(pal.TextPrimary)
			if desc != "" {
				ui.Text(c, desc).FontSize(11).TextColor(pal.TextSecondary)
			}
		})
		ui.Column(c).Shrink(0).Children(control)
	})
}

// settingsGroup is a titled card of rows, the unit of the settings page.
func settingsGroup(c *ui.Context, title string, build func()) {
	t := c.Theme()
	pal := win11Theme(t)
	ui.Column(c).Gap(6).Children(func() {
		if title != "" {
			ui.Text(c, title).FontSize(12).Bold().TextColor(pal.TextSecondary).Padding(0, 4)
		}
		ui.Column(c).Radius(10).Background(pal.Card).Border(1, pal.Border).
			Dividers(1, pal.Border).Children(build)
	})
}

// settingsSectionTitle names the active section in the window header.
func settingsSectionTitle(tab int) string {
	switch tab {
	case 1:
		return "翻译引擎"
	case 2:
		return "快捷键"
	case 3:
		return "外观与风格"
	case 4:
		return "关于"
	}
	return "常规"
}

func (a *app) settingsView(c *ui.Context) {
	t := fluentTheme(c)
	winW, _ := c.Size()
	pal := win11Theme(t)

	c.Root().Background(pal.Bg)

	status := a.settingsStatus
	if a.settingsDirty() && (status == "" || status == "已保存") {
		status = "有未保存的修改"
	}

	compactSidebar := winW < 640
	sidebarWidth := float32(200)
	padContentH := float32(24)
	if compactSidebar {
		sidebarWidth = 56
		padContentH = 12
	}
	titleH := float32(40)

	ui.Column(c).Fill().Children(func() {
		// Sidebar and content share the title bar band: the window controls
		// float over the content side, the sidebar starts under it.
		ui.Row(c).Grow(1).AlignItems(ui.Stretch).Children(func() {
			ui.Column(c).Width(sidebarWidth).Padding(titleH+8, 10, 10, 10).Gap(2).
				BorderWidth(0, 1, 0, 0).BorderColor(pal.Border).
				Background(pal.Surface).Children(func() {
				navItem := func(idx int, label string, icon *ui.SVG) {
					on := a.settingsTab == idx
					rad := float32(7)
					if isKDE() {
						rad = 5
					}
					btn := ui.ButtonBase(c).Height(32).Radius(rad).
						AlignItems(ui.Center).Gap(10).Tooltip(label)
					if compactSidebar {
						btn.Justify(ui.Center).Padding(0)
					} else {
						btn.Justify(ui.Start).Padding(6, 10)
					}
					if on {
						if isKDE() {
							btn.Background(pal.Accent.Alpha(0.20)).Border(1, pal.Accent.Alpha(0.40))
						} else {
							btn.Background(pal.Accent)
						}
					} else if btn.Hovered() {
						btn.Background(pal.SurfaceHover)
					}
					if btn.Clicked() {
						a.settingsTab = idx
						a.settingsScroll.Y = 0
					}
					btn.Children(func() {
						color := pal.TextPrimary
						if on {
							if isKDE() {
								color = pal.Accent
							} else {
								color = pal.AccentText
							}
						}
						ui.Box(c).Width(16).Height(16).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
							ui.Icon(c, icon).TextColor(color)
						})
						if !compactSidebar {
							ui.Text(c, label).FontSize(13).TextColor(color).SingleLine()
						}
					})
				}

				navItem(0, "常规", iconGeneral)
				navItem(1, "翻译引擎", iconTranslate)
				navItem(2, "快捷键", iconKeyboard)
				navItem(3, "外观与风格", iconAppearance)
				navItem(4, "关于", iconAbout)
			})

			// Content: a draggable section header, then the scrolling page.
			ui.Column(c).Grow(1).Children(func() {
				ui.Row(c).Height(titleH).Padding(0, padContentH, 0, padContentH).
					AlignItems(ui.Center).DragWindow().Children(func() {
					ui.Text(c, settingsSectionTitle(a.settingsTab)).
						FontSize(15).Bold().TextColor(pal.TextPrimary)
				})
				ui.Scroll(c).Grow(1).TrackScroll(&a.settingsScroll).Children(func() {
					ui.Column(c).Padding(4, padContentH, 24, padContentH).Gap(18).Children(func() {
						switch a.settingsTab {
						case 0:
							// 常规
							settingsGroup(c, "行为", func() {
								settingRow(c, "登录时自动启动", "开机后自动在后台托盘就绪", func() {
									ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
										txt := "关"
										if a.openAtLogin {
											txt = "开"
										}
										ui.Text(c, txt).FontSize(13).TextColor(pal.TextSecondary)
										previous := a.openAtLogin
										if ui.Switch(c, &a.openAtLogin).Changed() {
											if err := a.updateOpenAtLogin(a.openAtLogin, mygo.App.SetOpenAtLogin); err != nil {
												a.openAtLogin = previous
												a.settingsStatus = "开机启动设置失败：" + err.Error()
											} else {
												a.settingsStatus = "开机启动已更新"
											}
										}
									})
								})

								settingRow(c, "选词自动翻译", "选中文字后从任务栏弹出翻译面板", func() {
									ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
										txt := "关"
										enabled := !a.form.DisableSelectionToolbar
										if enabled {
											txt = "开"
										}
										ui.Text(c, txt).FontSize(13).TextColor(pal.TextSecondary)
										ui.Switch(c, &enabled).OnClick(func() { a.form.DisableSelectionToolbar = !enabled })
									})
								})

								settingRow(c, "面板默认固定", "点击其他窗口时保持面板打开", func() {
									ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
										txt := "关"
										if a.form.PopupPinned {
											txt = "开"
										}
										ui.Text(c, txt).FontSize(13).TextColor(pal.TextSecondary)
										ui.Switch(c, &a.form.PopupPinned)
									})
								})

							})

							settingsGroup(c, "语言首选项", func() {
								settingRow(c, "默认目标语言", "选中文本后默认翻译成的语言", func() {
									ui.Select(c, &a.form.TargetLang, targetLanguages).Width(160)
								})
								settingRow(c, "源语言", "留空或填写「自动检测」即可", func() {
									ui.TextInput(c, &a.form.SourceLang).Width(160).Placeholder("自动检测")
								})
							})

						case 1:
							// 翻译引擎
							settingsGroup(c, "默认服务", func() {
								settingRow(c, "翻译服务", "选择默认使用的在线翻译引擎", func() {
									activeName := a.providerName
									if activeName == "" {
										activeName = "OpenAI Compatible"
									}
									ui.Box(c).Padding(4, 10).Radius(6).Background(pal.Surface).Border(1, pal.Border).Children(func() {
										ui.Text(c, activeName).FontSize(12).TextColor(pal.TextPrimary)
									})
								})
							})

							ui.Row(c).AlignItems(ui.Center).Children(func() {
								ui.Text(c, "引擎管理与多引擎对比").FontSize(12).Bold().TextColor(pal.TextSecondary)
								ui.Spacer(c)
								ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
									ui.Text(c, "多引擎并发").FontSize(11).TextColor(pal.TextSecondary)
									ui.Switch(c, &a.form.MultiEngine)
								})
							})

							removeAt, moveFrom, moveTo := -1, -1, -1
							for i := range a.form.Providers {
								p := &a.form.Providers[i]
								armed := a.providerDeleteArmed == p.ID
								ui.Column(c.Key(p.ID)).Gap(8).Padding(12, 14).Radius(10).
									Background(pal.Card).Border(1, pal.Border).Children(func() {
									ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
										ui.Switch(c, &p.Enabled)
										ui.Text(c, providerKindName(p.Kind)).Bold().FontSize(13)
										ui.Badge(c, fmt.Sprintf("优先级 %d", i+1)).
											Background(pal.Accent.Alpha(0.12)).TextColor(pal.Accent)
										ui.Spacer(c)
										if ui.Button(c, "上移").Tooltip("提高优先级").Disabled(i == 0).Clicked() {
											moveFrom, moveTo = i, i-1
											a.providerDeleteArmed = ""
											a.settingsStatus = "已调整优先级，保存后生效"
										}
										if ui.Button(c, "下移").Tooltip("降低优先级").
											Disabled(i == len(a.form.Providers)-1).Clicked() {
											moveFrom, moveTo = i, i+1
											a.providerDeleteArmed = ""
											a.settingsStatus = "已调整优先级，保存后生效"
										}
										label := "删除"
										if armed {
											label = "确认删除"
										}
										del := ui.Button(c, label)
										if armed {
											del.Background(t.Danger).TextColor(ui.RGB(255, 255, 255))
										}
										if del.Tooltip("删除这个翻译服务").Clicked() {
											if armed {
												removeAt = i
												a.providerDeleteArmed = ""
												a.settingsStatus = "已移除「" + p.Name + "」，保存后生效"
											} else {
												a.providerDeleteArmed = p.ID
												a.settingsStatus = "再点一次「确认删除」移除「" + p.Name + "」"
											}
										}
									})

									ui.Row(c).Gap(10).Children(func() {
										ui.Column(c).Grow(1).Children(func() {
											ui.Field(c, "服务名称", func() {
												ui.TextInput(c, &p.Name).Placeholder("自定义显示名称")
											})
										})
										switch p.Kind {
										case providerOpenAI:
											ui.Column(c).Grow(1).Children(func() {
												ui.Field(c, "模型", func() {
													ui.TextInput(c, &p.Model).Placeholder("gpt-4o-mini")
												})
											})
										}
									})

									switch p.Kind {
									case providerOpenAI:
										ui.Field(c, "接口地址 Base URL", func() {
											ui.TextInput(c, &p.BaseURL).Placeholder("https://api.openai.com/v1")
										})
										ui.Checkbox(c, &p.Vision, "支持视觉模型截图翻译")
									case providerAzure:
										ui.Row(c).Gap(10).Children(func() {
											ui.Column(c).Grow(1).Children(func() {
												ui.Field(c, "接口地址（可选）", func() {
													ui.TextInput(c, &p.Endpoint).Placeholder(defaultAzureEndpoint)
												})
											})
											ui.Column(c).Width(140).Children(func() {
												ui.Field(c, "区域（可选）", func() {
													ui.TextInput(c, &p.Region).Placeholder("eastasia")
												})
											})
										})
									case providerBaidu:
										ui.Row(c).Gap(10).Children(func() {
											ui.Column(c).Grow(1).Children(func() {
												ui.Field(c, "接口地址（可选）", func() {
													ui.TextInput(c, &p.Endpoint).Placeholder(defaultBaiduEndpoint)
												})
											})
											ui.Column(c).Width(140).Children(func() {
												ui.Field(c, "APP ID", func() {
													ui.TextInput(c, &p.AppID)
												})
											})
										})
									}

									keyLabel := "API Key"
									keyPlaceholder := "sk-…"
									switch p.Kind {
									case providerAzure:
										keyLabel = "订阅 Key"
										keyPlaceholder = "Azure 订阅密钥"
									case providerBaidu:
										keyLabel = "密钥 Secret"
										keyPlaceholder = "百度翻译密钥"
									}
									ui.Field(c, keyLabel, func() {
										ui.TextInput(c, &p.Key).Password().Placeholder(keyPlaceholder)
									}).Description("密钥保存在 系统密钥服务，不写入明文配置文件。")
								})
								ui.Box(c).Height(6)
							}

							if removeAt >= 0 {
								a.form.Providers = append(a.form.Providers[:removeAt], a.form.Providers[removeAt+1:]...)
							} else if moveFrom >= 0 {
								a.form.Providers[moveFrom], a.form.Providers[moveTo] = a.form.Providers[moveTo], a.form.Providers[moveFrom]
							}

							ui.Row(c).Gap(8).Wrap().Children(func() {
								if ui.Button(c, "+ OpenAI Compatible").Clicked() {
									a.addFormProvider(providerOpenAI)
								}
								if ui.Button(c, "+ Azure 翻译").Clicked() {
									a.addFormProvider(providerAzure)
								}
								if ui.Button(c, "+ 百度翻译").Clicked() {
									a.addFormProvider(providerBaidu)
								}
							})

						case 2:
							// 快捷键
							settingsGroup(c, "全局快捷键", func() {
								settingRow(c, "翻译选中文本", "选中文本后按快捷键立即呼出翻译浮窗", func() {
									ui.TextInput(c, &a.form.Hotkey).Width(160).Placeholder("Alt+F")
								})

								settingRow(c, "截图翻译", "截取屏幕任意区域并调用视觉模型识别翻译", func() {
									ui.TextInput(c, &a.form.ScreenshotHotkey).Width(160).Placeholder("Alt+S")
								})

								settingRow(c, "输入翻译", "打开托盘面板的输入翻译页", func() {
									ui.TextInput(c, &a.form.InputHotkey).Width(160).Placeholder("Alt+D")
								})
							})

						case 3:
							// 外观与风格
							settingsGroup(c, "界面", func() {
								settingRow(c, "主题", "保存后立即生效；跟随系统明暗模式切换", func() {
									ui.Row(c).Gap(6).Children(func() {
										for _, opt := range []struct{ value, label string }{
											{config.ThemeSystem, "跟随系统"},
											{config.ThemeLight, "浅色"},
											{config.ThemeDark, "深色"},
										} {
											btn := ui.Button(c.Key("theme-"+opt.value), opt.label)
											if a.form.Theme == opt.value {
												btn.Background(pal.Accent).TextColor(ui.RGB(255, 255, 255))
											}
											if btn.Clicked() {
												a.form.Theme = opt.value
											}
										}
									})
								})
								settingRow(c, "默认模式", "弹窗顶部可以随时切换", func() {
									ui.Row(c).Gap(6).Wrap().Children(func() {
										for _, m := range a.form.AllModes() {
											btn := ui.Button(c.Key("default-mode-"+m.ID), m.Name)
											if a.form.ActiveMode == m.ID {
												btn.Background(pal.Accent).TextColor(ui.RGB(255, 255, 255))
											}
											if btn.Clicked() {
												a.form.ActiveMode = m.ID
											}
										}
									})
								})
							})

							settingsGroup(c, "提示词模式", func() {
								if !a.settingsModesEditing && len(a.form.Modes) == 0 {
									ui.Column(c).Padding(14).Gap(8).Children(func() {
										ui.Text(c, "内置模式").FontSize(13).Bold().TextColor(pal.TextPrimary)
										names := make([]string, 0, 4)
										for _, m := range a.form.AllModes() {
											names = append(names, m.Name)
										}
										ui.Text(c, strings.Join(names, " · ")).FontSize(11).TextColor(pal.TextSecondary)
										ui.Text(c, "需要改写提示词或增加新模式时，可以把内置模式复制成可编辑列表。").FontSize(11).TextColor(pal.TextSecondary)
										if ui.Button(c, "自定义模式").Clicked() {
											a.form.Modes = a.form.AllModes()
											if !containsMode(a.form.Modes, a.form.ActiveMode) {
												a.form.ActiveMode = a.form.Modes[0].ID
											}
											a.settingsModesEditing = true
											a.settingsStatus = "正在编辑自定义模式，保存后生效"
										}
									})
								} else {
									removeMode := -1
									ui.Column(c).Gap(10).Padding(12).Children(func() {
										for i := range a.form.Modes {
											m := &a.form.Modes[i]
											ui.Column(c.Key(m.ID)).Gap(6).Padding(12, 14).Radius(8).
												Background(pal.Surface).Border(1, pal.Border).Children(func() {
												ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
													ui.TextInput(c, &m.Name).Width(150).Placeholder("模式名称")
													ui.Text(c, m.ID).FontSize(11).TextColor(pal.TextSecondary)
													ui.Spacer(c)
													for _, kind := range []struct{ value, label string }{
														{config.ModeMaterial, "材料"},
														{config.ModeInstruction, "指令"},
													} {
														kindBtn := ui.Button(c.Key("kind-"+m.ID+"-"+kind.value), kind.label)
														if m.Kind == kind.value {
															kindBtn.Background(pal.Accent).TextColor(ui.RGB(255, 255, 255))
														}
														if kindBtn.Clicked() {
															m.Kind = kind.value
														}
													}
													if ui.Button(c, "删除").Disabled(len(a.form.Modes) == 1).Clicked() {
														removeMode = i
													}
												})
												ui.TextArea(c, &m.Prompt).Lines(2, 4)
											})
										}
										ui.Row(c).Gap(8).Wrap().Children(func() {
											if ui.Button(c, "+ 添加模式").Clicked() {
												a.form.Modes = append(a.form.Modes, newMode(a.form))
											}
											if ui.Button(c, "恢复内置模式").Clicked() {
												a.form.Modes = nil
												a.settingsModesEditing = false
												a.form.ActiveMode = "translate"
												a.settingsStatus = "已恢复内置模式，保存后生效"
											}
										})
									})
									if removeMode >= 0 {
										removed := a.form.Modes[removeMode]
										a.form.Modes = append(a.form.Modes[:removeMode], a.form.Modes[removeMode+1:]...)
										if a.form.ActiveMode == removed.ID {
											if len(a.form.Modes) > 0 {
												a.form.ActiveMode = a.form.Modes[0].ID
											} else {
												a.form.ActiveMode = "translate"
											}
										}
									}
								}
							})

							settingsGroup(c, "翻译系统提示词", func() {
								ui.Column(c).Padding(14).Children(func() {
									ui.Field(c, "内置翻译提示词", func() {
										ui.TextArea(c, &a.form.SystemPrompt).Lines(3, 6)
									}).Description("{{target}} 代表目标语言，{{source}} 代表源语言。自定义模式后，这段提示词会复制到「翻译」模式里。")
								})
							})

						case 4:
							// 关于
							ui.Column(c).Radius(10).Background(pal.Card).Border(1, pal.Border).Padding(16).Gap(10).Children(func() {
								ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
									ui.Box(c).Width(36).Height(36).Radius(8).Background(pal.Accent).
										AlignItems(ui.Center).Justify(ui.Center).Children(func() {
										ui.Icon(c, iconTranslate).TextColor(ui.RGB(255, 255, 255))
									})
									ui.Column(c).Children(func() {
										ui.Text(c, "Hayami Translator 划词翻译").Bold().FontSize(15).TextColor(pal.TextPrimary)
										ui.Text(c, "轻量托盘划词翻译工具").FontSize(12).TextColor(pal.TextSecondary)
									})
								})

								ui.Text(c, "安全特性：所有 API Key 均存储于 系统密钥服务，配置文件与日志中绝不存储明文敏感信息。").
									FontSize(12).TextColor(pal.TextSecondary)

								if dir := userDataDir(); dir != "" {
									ui.Text(c, "数据目录："+dir).FontSize(11).TextColor(pal.TextSecondary)
								}
							})
						}
					})
				})
			})
		})

		// Bottom Save & Action Bar
		ui.Row(c).Height(48).Padding(0, 16).Gap(10).AlignItems(ui.Center).
			BorderWidth(1, 0, 0, 0).BorderColor(pal.Border).
			Background(pal.Footer).Children(func() {
			if status != "" {
				ui.Text(c, status).FontSize(12).
					TextColor(statusColor(t, status)).SingleLine().Shrink(1)
			}
			if a.hotkeyErr != "" {
				ui.Text(c, a.hotkeyErr).FontSize(12).TextColor(t.Danger).SingleLine().Shrink(1)
			}
			ui.Spacer(c)
			ui.Button(c, "历史记录").OnClick(a.showHistory)
			ui.Button(c, "关闭").OnClick(a.requestCloseSettings)
			ui.PrimaryButton(c, "保存设置").OnClick(a.saveSettings)
		})
	})

	// Closing with an unsaved draft must stop and ask: saving, discarding
	// and canceling are all one click away, and Escape cancels.
	switch ui.AlertDialog(c, &a.settingsCloseAsk, "有未保存的修改",
		"保存后关闭，或放弃这次的修改。",
		"取消", "放弃修改", "保存并关闭") {
	case 1:
		a.form = a.cfg.Clone()
		a.toolbarEnabled = !a.cfg.DisableSelectionToolbar
		a.providerDeleteArmed = ""
		a.settingsStatus = ""
		a.hidePopup()
	case 2:
		a.saveSettings()
		if !a.settingsDirty() {
			a.hidePopup()
		}
	}
}

// requestCloseSettings closes the settings window unless it holds unsaved
// edits, which the dialog in settingsView then asks about.
func (a *app) requestCloseSettings() {
	if !a.settingsDirty() {
		a.hidePopup()
		return
	}
	a.settingsCloseAsk = true
	a.refreshSettingsWindow()
}
