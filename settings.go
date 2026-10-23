package main

import (
	"fmt"
	"reflect"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

var triggerModes = []string{
	"显示图标，点击翻译",
	"取词即译气泡",
	"直接弹出完整翻译",
}

// settingsDirty reports whether the draft differs from the running config.
func (a *app) settingsDirty() bool {
	f, c := a.form, a.cfg
	if len(f.Providers) == 0 && len(c.Providers) == 0 {
		f.Providers = nil
		c.Providers = nil
	}
	return !reflect.DeepEqual(f, c) ||
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

// settingRow renders a responsive Windows 11 Fluent settings row (left label+desc, right control)
func settingRow(c *ui.Context, title, desc string, control func()) {
	t := c.Theme()
	_, _, _, border, textPrimary, textSecondary, _, _ := win11Theme(t)

	ui.Row(c).Padding(12, 16).AlignItems(ui.Center).Gap(12).
		BorderWidth(0, 0, 1, 0).BorderColor(border).Children(func() {
		ui.Column(c).Grow(1).Shrink(1).Gap(2).Justify(ui.Center).Children(func() {
			ui.Text(c, title).FontSize(14).TextColor(textPrimary)
			if desc != "" {
				ui.Text(c, desc).FontSize(12).TextColor(textSecondary)
			}
		})
		ui.Column(c).Shrink(0).Children(control)
	})
}

func (a *app) settingsView(c *ui.Context) {
	t := c.Theme()
	winW, _ := c.Size()
	bg, surface, cardBg, border, textPrimary, textSecondary, accent, footerBg := win11Theme(t)

	c.Root().Background(bg)

	status := a.settingsStatus
	if a.settingsDirty() && (status == "" || status == "已保存") {
		status = "有未保存的修改"
	}

	compactSidebar := winW < 640
	sidebarWidth := float32(210)
	padContentH := float32(24)
	if compactSidebar {
		sidebarWidth = 56
		padContentH = 12
	}

	ui.Column(c).Fill().Children(func() {
		// Window Title Header
		ui.Row(c).Height(38).Padding(0, 16).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "划词翻译 设置").FontSize(13).Bold().TextColor(textPrimary)
		})

		// Main Layout: Left Sidebar + Right Content Area
		ui.Row(c).Grow(1).AlignItems(ui.Stretch).Children(func() {
			// Left Navigation Sidebar (Responsive full or compact icon mode)
			ui.Column(c).Width(sidebarWidth).Padding(8, 8).Gap(2).
				BorderWidth(0, 1, 0, 0).BorderColor(border).
				Background(bg).Children(func() {
				navItem := func(idx int, label string, icon *ui.SVG) {
					on := a.settingsTab == idx
					btn := ui.ButtonBase(c).Height(40).Radius(4).
						AlignItems(ui.Center).Gap(10).Tooltip(label)
					if compactSidebar {
						btn.Justify(ui.Center).Padding(0)
					} else {
						btn.Justify(ui.Start).Padding(0, 10)
					}
					if on {
						btn.Background(ui.RGBA(0, 0, 0, 0.05))
					}
					if btn.Clicked() {
						a.settingsTab = idx
						a.settingsScroll.Y = 0
					}
					btn.Children(func() {
						if on && !compactSidebar {
							ui.Box(c).Width(3).Height(20).Radius(2).Background(accent)
						}
						ui.Box(c).Width(18).Height(18).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
							ui.Icon(c, icon).TextColor(accent)
						})
						if !compactSidebar {
							ui.Text(c, label).FontSize(14).TextColor(textPrimary).SingleLine()
						}
					})
				}

				navItem(0, "常规", iconGeneral)
				navItem(1, "翻译引擎", iconTranslate)
				navItem(2, "快捷键", iconKeyboard)
				navItem(3, "外观与风格", iconAppearance)
				navItem(4, "关于", iconAbout)
			})

			// Right Content Area (Fluid & Responsive Scroll)
			ui.Column(c).Grow(1).Padding(14, padContentH, 16, padContentH).Children(func() {
				ui.Scroll(c).Grow(1).TrackScroll(&a.settingsScroll).Padding(0, 0, 10, 0).Children(func() {
					switch a.settingsTab {
					case 0:
						// 常规
						ui.Text(c, "常规").FontSize(24).Bold().TextColor(textPrimary)
						ui.Box(c).Height(12)

						ui.Text(c, "行为").FontSize(14).Bold().TextColor(textPrimary)
						ui.Box(c).Height(6)
						ui.Column(c).Radius(6).Background(cardBg).Border(1, border).Children(func() {
							settingRow(c, "登录 Windows 时自动启动", "开机后自动在后台托盘就绪", func() {
								ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
									txt := "关"
									if a.openAtLogin {
										txt = "开"
									}
									ui.Text(c, txt).FontSize(13).TextColor(textSecondary)
									if ui.Switch(c, &a.openAtLogin).Changed() {
										if err := mygo.App.SetOpenAtLogin(a.openAtLogin); err != nil {
											a.settingsStatus = "开机启动设置失败：" + err.Error()
										} else {
											a.settingsStatus = "开机启动已更新"
										}
									}
								})
							})

							settingRow(c, "自动朗读译文", "获得翻译结果后自动发音朗读", func() {
								ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
									txt := "关"
									if a.form.AutoSpeak {
										txt = "开"
									}
									ui.Text(c, txt).FontSize(13).TextColor(textSecondary)
									ui.Switch(c, &a.form.AutoSpeak)
								})
							})

							settingRow(c, "划词即译助手", "鼠标划选文本后在光标旁显示翻译悬浮按钮", func() {
								ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
									txt := "关"
									if a.toolbarEnabled {
										txt = "开"
									}
									ui.Text(c, txt).FontSize(13).TextColor(textSecondary)
									ui.Switch(c, &a.toolbarEnabled)
								})
							})

							settingRow(c, "浮窗默认固定", "打开翻译浮窗时默认保持置顶且点击外部不自动关闭", func() {
								ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
									txt := "关"
									if a.form.PopupPinned {
										txt = "开"
									}
									ui.Text(c, txt).FontSize(13).TextColor(textSecondary)
									ui.Switch(c, &a.form.PopupPinned)
								})
							})
						})

						ui.Box(c).Height(16)
						ui.Text(c, "语言首选项").FontSize(14).Bold().TextColor(textPrimary)
						ui.Box(c).Height(6)
						ui.Column(c).Radius(6).Background(cardBg).Border(1, border).Children(func() {
							settingRow(c, "默认目标语言", "选中文本后默认翻译成的语言", func() {
								ui.Select(c, &a.form.TargetLang, targetLanguages).Width(160)
							})
							settingRow(c, "源语言", "留空或填写「自动检测」即可", func() {
								ui.TextInput(c, &a.form.SourceLang).Width(160).Placeholder("自动检测")
							})
						})

					case 1:
						// 翻译引擎
						ui.Text(c, "翻译引擎").FontSize(24).Bold().TextColor(textPrimary)
						ui.Box(c).Height(12)

						// 核心配置卡片
						ui.Column(c).Radius(6).Background(cardBg).Border(1, border).Children(func() {
							settingRow(c, "翻译服务", "选择默认使用的在线翻译引擎", func() {
								activeName := a.providerName
								if activeName == "" {
									activeName = "OpenAI Compatible"
								}
								ui.Box(c).Padding(4, 10).Radius(4).Background(surface).Border(1, border).Children(func() {
									ui.Text(c, activeName).FontSize(13).TextColor(textPrimary)
								})
							})

							settingRow(c, "目标语言", "划词后翻译成的语言", func() {
								ui.Select(c, &a.form.TargetLang, targetLanguages).Width(160)
							})

							settingRow(c, "触发方式", "选中文本后如何显示翻译", func() {
								ui.Select(c, &a.form.TriggerMode, triggerModes).Width(160)
							})
						})

						ui.Box(c).Height(18)
						ui.Text(c, "行为").FontSize(14).Bold().TextColor(textPrimary)
						ui.Box(c).Height(6)
						ui.Column(c).Radius(6).Background(cardBg).Border(1, border).Children(func() {
							settingRow(c, "登录 Windows 时自动启动", "开机后自动在后台就绪", func() {
								ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
									txt := "关"
									if a.openAtLogin {
										txt = "开"
									}
									ui.Text(c, txt).FontSize(13).TextColor(textSecondary)
									if ui.Switch(c, &a.openAtLogin).Changed() {
										if err := mygo.App.SetOpenAtLogin(a.openAtLogin); err != nil {
											a.settingsStatus = "开机启动设置失败：" + err.Error()
										} else {
											a.settingsStatus = "开机启动已更新"
										}
									}
								})
							})

							settingRow(c, "自动朗读译文", "翻译成功后自动进行语音朗读", func() {
								ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
									txt := "关"
									if a.form.AutoSpeak {
										txt = "开"
									}
									ui.Text(c, txt).FontSize(13).TextColor(textSecondary)
									ui.Switch(c, &a.form.AutoSpeak)
								})
							})

							settingRow(c, "全局快捷键：翻译选中文本", "划选文本后呼出翻译浮窗", func() {
								hk := a.form.Hotkey
								if hk == "" {
									hk = "Ctrl + Alt + T"
								}
								ui.Box(c).Padding(3, 8).Radius(4).Background(surface).Border(1, border).Children(func() {
									ui.Text(c, hk).FontSize(12).Bold().TextColor(textPrimary)
								})
							})
						})

						ui.Box(c).Height(18)
						ui.Row(c).AlignItems(ui.Center).Children(func() {
							ui.Text(c, "引擎管理与多引擎对比").FontSize(14).Bold().TextColor(textPrimary)
							ui.Spacer(c)
							ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
								ui.Text(c, "多引擎并发").FontSize(12).TextColor(textSecondary)
								ui.Switch(c, &a.form.MultiEngine)
							})
						})
						ui.Box(c).Height(6)

						removeAt, moveFrom, moveTo := -1, -1, -1
						for i := range a.form.Providers {
							p := &a.form.Providers[i]
							armed := a.providerDeleteArmed == p.ID
							ui.Column(c.Key(p.ID)).Gap(8).Padding(12, 14).Radius(6).
								Background(surface).Border(1, border).Children(func() {
								ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
									ui.Switch(c, &p.Enabled)
									ui.Text(c, providerKindName(p.Kind)).Bold().FontSize(13)
									ui.Badge(c, fmt.Sprintf("优先级 %d", i+1)).
										Background(accent.Alpha(0.12)).TextColor(accent)
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
								}).Description("密钥保存在 Windows 凭据管理器，不写入明文配置文件。")
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
						ui.Text(c, "快捷键").FontSize(24).Bold().TextColor(textPrimary)
						ui.Box(c).Height(12)

						ui.Column(c).Radius(6).Background(cardBg).Border(1, border).Children(func() {
							settingRow(c, "全局快捷键：翻译选中文本", "选中文本后按快捷键立即呼出翻译浮窗", func() {
								ui.TextInput(c, &a.form.Hotkey).Width(160).Placeholder("Ctrl+Alt+T")
							})

							settingRow(c, "全局快捷键：截图翻译", "截取屏幕任意区域并调用视觉模型识别翻译", func() {
								ui.TextInput(c, &a.form.ScreenshotHotkey).Width(160).Placeholder("Alt+Shift+S")
							})

							settingRow(c, "输入翻译快捷键", "快速呼出独立的双栏输入翻译窗口", func() {
								ui.TextInput(c, &a.form.InputHotkey).Width(160).Placeholder("Ctrl+Alt+I")
							})
						})

					case 3:
						// 外观与风格
						ui.Text(c, "外观与风格").FontSize(24).Bold().TextColor(textPrimary)
						ui.Box(c).Height(12)

						ui.Text(c, "系统提示词").FontSize(14).Bold().TextColor(textPrimary)
						ui.Box(c).Height(6)
						ui.Column(c).Radius(6).Background(cardBg).Border(1, border).Padding(14).Children(func() {
							ui.Field(c, "翻译系统提示词", func() {
								ui.TextArea(c, &a.form.SystemPrompt).Lines(3, 6)
							}).Description("{{target}} 代表目标语言，{{source}} 代表源语言。")
						})

					case 4:
						// 关于
						ui.Text(c, "关于").FontSize(24).Bold().TextColor(textPrimary)
						ui.Box(c).Height(12)

						ui.Column(c).Radius(6).Background(cardBg).Border(1, border).Padding(16).Gap(10).Children(func() {
							ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
								ui.Box(c).Width(36).Height(36).Radius(8).Background(accent).
									AlignItems(ui.Center).Justify(ui.Center).Children(func() {
									ui.Icon(c, iconTranslate).TextColor(ui.RGB(255, 255, 255))
								})
								ui.Column(c).Children(func() {
									ui.Text(c, "Hayami Translator 划词翻译").Bold().FontSize(15).TextColor(textPrimary)
									ui.Text(c, "Windows 11 原生风格轻量划词翻译工具").FontSize(12).TextColor(textSecondary)
								})
							})

							ui.Text(c, "安全特性：所有 API Key 均存储于 Windows 凭据管理器（Credential Manager），配置文件与日志中绝不存储明文敏感信息。").
								FontSize(12).TextColor(textSecondary)

							if dir := userDataDir(); dir != "" {
								ui.Text(c, "数据目录："+dir).FontSize(11).TextColor(textSecondary)
							}
						})
					}
				})
			})
		})

		// Bottom Save & Action Bar
		ui.Row(c).Height(48).Padding(0, 16).Gap(10).AlignItems(ui.Center).
			BorderWidth(1, 0, 0, 0).BorderColor(border).
			Background(footerBg).Children(func() {
			if status != "" {
				ui.Text(c, status).FontSize(12).
					TextColor(statusColor(t, status)).SingleLine().Shrink(1)
			}
			if a.hotkeyErr != "" {
				ui.Text(c, a.hotkeyErr).FontSize(12).TextColor(t.Danger).SingleLine().Shrink(1)
			}
			ui.Spacer(c)
			if ui.Button(c, "历史记录").Clicked() {
				a.showHistory()
			}
			if ui.Button(c, "关闭").Clicked() {
				a.settings.Hide()
			}
			if ui.PrimaryButton(c, "保存设置").Clicked() {
				a.saveSettings()
			}
		})
	})
}
