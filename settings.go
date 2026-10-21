package main

import (
	"fmt"
	"reflect"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// settingsDirty reports whether the draft differs from the running config.
// openAtLogin applies immediately, so it is not part of this check.
func (a *app) settingsDirty() bool {
	return !reflect.DeepEqual(a.form, a.cfg) ||
		a.toolbarEnabled != !a.cfg.DisableSelectionToolbar
}

// settingsView edits the endpoint, the model, the key, the languages and the
// shortcuts. Nothing is written until Save is pressed.
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

func (a *app) settingsView(c *ui.Context) {
	t := c.Theme()
	c.Root().Background(t.Background)

	status := a.settingsStatus
	if a.settingsDirty() && (status == "" || status == "已保存") {
		status = "有未保存的修改"
	}
	field := func(label string, build func()) ui.Element {
		return ui.Field(c, label, build)
	}

	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Padding(16, 20).Gap(12).AlignItems(ui.Center).
			BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
			ui.Column(c).Gap(2).Justify(ui.Center).Children(func() {
				ui.Text(c, "设置").FontSize(20).Bold()
				ui.Text(c, "翻译服务、语言和快捷键").FontSize(11).TextColor(t.TextMuted)
			})
			ui.Spacer(c)
			ui.Badge(c, "密钥仅存本机").
				Background(t.Surface).TextColor(t.TextMuted)
		})

		ui.Scroll(c).Grow(1).Padding(18, 20).Children(func() {
			ui.Column(c).Gap(22).Children(func() {
				section(c, "翻译服务", "从上到下按优先级尝试；某个服务在返回内容前失败时，自动换下一个。", func() {
					removeAt, moveFrom, moveTo := -1, -1, -1
					for i := range a.form.Providers {
						p := &a.form.Providers[i]
						armed := a.providerDeleteArmed == p.ID
						ui.Column(c).Key(p.ID).Gap(10).Padding(14).Radius(10).
							Background(t.Surface).Border(1, t.Border).Children(func() {
							ui.Row(c).Gap(8).Children(func() {
								ui.Checkbox(c, &p.Enabled, "启用")
								ui.Text(c, providerKindName(p.Kind)).Bold()
								ui.Badge(c, fmt.Sprintf("优先级 %d", i+1)).
									Background(t.Accent.Alpha(0.12)).TextColor(t.Accent)
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
									del.Background(t.Danger).TextColor(t.AccentText)
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

							field("名称", func() {
								ui.TextInput(c, &p.Name).Placeholder("用于区分多个同类服务")
							})

							switch p.Kind {
							case providerOpenAI:
								field("接口地址", func() {
									ui.TextInput(c, &p.BaseURL).Placeholder("https://api.openai.com/v1")
								})
								field("模型", func() {
									ui.TextInput(c, &p.Model).Placeholder("gpt-4o-mini")
								})
								ui.Checkbox(c, &p.Vision, "支持图片（截图翻译）")
							case providerAzure:
								ui.Row(c).Gap(12).Children(func() {
									ui.Column(c).Grow(1).Children(func() {
										field("接口地址（可选）", func() {
											ui.TextInput(c, &p.Endpoint).Placeholder(defaultAzureEndpoint)
										})
									})
									ui.Column(c).Width(170).Children(func() {
										field("区域（可选）", func() {
											ui.TextInput(c, &p.Region).Placeholder("eastasia")
										})
									})
								})
							case providerBaidu:
								field("接口地址（可选）", func() {
									ui.TextInput(c, &p.Endpoint).Placeholder(defaultBaiduEndpoint)
								})
								field("APP ID", func() {
									ui.TextInput(c, &p.AppID)
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
							field(keyLabel, func() {
								ui.TextInput(c, &p.Key).Password().Placeholder(keyPlaceholder)
							}).Description("保存在 Windows 凭据管理器，不写入配置文件。")
						})
					}

					// Apply list changes after rendering, so the pointers above
					// stay valid while the rows are built.
					if removeAt >= 0 {
						a.form.Providers = append(a.form.Providers[:removeAt], a.form.Providers[removeAt+1:]...)
					} else if moveFrom >= 0 {
						a.form.Providers[moveFrom], a.form.Providers[moveTo] = a.form.Providers[moveTo], a.form.Providers[moveFrom]
					}

					if len(a.form.Providers) == 0 {
						card(c, func() {
							ui.Text(c, "还没有翻译服务").Bold()
							ui.Text(c, "至少添加一个服务才能开始翻译。").
								FontSize(12).TextColor(t.TextMuted)
						})
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
				})

				section(c, "语言", "自动方向会在中文原文时译成英文，其他语言译成简体中文。", func() {
					card(c, func() {
						ui.Row(c).Gap(14).Children(func() {
							ui.Column(c).Grow(1).Gap(4).Children(func() {
								field("目标语言", func() {
									ui.Select(c, &a.form.TargetLang, targetLanguages).Label("目标语言")
								})
								if a.form.TargetLang == autoTarget {
									ui.Text(c, "中文 → 英文，其他 → 简体中文").
										FontSize(11).TextColor(t.TextMuted)
								}
							})
							ui.Column(c).Grow(1).Gap(4).Children(func() {
								field("源语言", func() {
									ui.TextInput(c, &a.form.SourceLang).Placeholder("自动检测")
								})
								ui.Text(c, "留空或填写「自动检测」即可。").
									FontSize(11).TextColor(t.TextMuted)
							})
						})
					})
				})

				section(c, "快捷键", "使用 Ctrl、Alt、Shift、CmdOrCtrl 加一个按键。保存后立即生效。", func() {
					card(c, func() {
						ui.Row(c).Gap(14).Children(func() {
							ui.Column(c).Grow(1).Children(func() {
								field("翻译选中文本", func() {
									ui.TextInput(c, &a.form.Hotkey).Placeholder("Alt+Shift+D")
								})
							})
							ui.Column(c).Grow(1).Children(func() {
								field("截图翻译", func() {
									ui.TextInput(c, &a.form.ScreenshotHotkey).Placeholder("Alt+Shift+S")
								})
							})
						})
					})
				})

				section(c, "翻译风格", "提示词会随请求发送给支持系统提示词的服务。", func() {
					card(c, func() {
						ui.Field(c, "系统提示词", func() {
							ui.TextArea(c, &a.form.SystemPrompt).Lines(3, 8)
						}).Description("{{target}} 会替换成目标语言；{{source}} 会替换成源语言。")
					})
				})

				section(c, "行为", "这些选项会影响程序启动和划词助手。", func() {
					card(c, func() {
						if ui.Checkbox(c, &a.openAtLogin, "开机启动").Changed() {
							if err := mygo.App.SetOpenAtLogin(a.openAtLogin); err != nil {
								a.settingsStatus = "开机启动设置失败：" + err.Error()
							} else {
								a.settingsStatus = "开机启动已更新"
							}
						}
						ui.Checkbox(c, &a.toolbarEnabled, "拖动选择后显示划词助手")
						ui.Text(c, "拖选文字后松开鼠标，选区旁会出现「文A / 复制 / ×」工具条，保存设置后生效。系统终端窗口会跳过，避免触发 Ctrl+C。").
							FontSize(11).TextColor(t.TextMuted)
					})
				})

				if dir := userDataDir(); dir != "" {
					ui.Text(c, "数据目录："+dir).
						FontSize(11).TextColor(t.TextMuted).SingleLine().Tooltip(dir)
				}
			})
		})

		ui.Row(c).Padding(14, 20).Gap(12).AlignItems(ui.Center).
			BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Children(func() {
			ui.Column(c).Grow(1).Gap(2).Justify(ui.Center).Children(func() {
				if status != "" {
					ui.Text(c, status).FontSize(12).
						TextColor(statusColor(t, status)).SingleLine().
						Tooltip(status)
				}
				if a.hotkeyErr != "" {
					ui.Text(c, a.hotkeyErr).FontSize(11).TextColor(t.Danger).SingleLine().
						Tooltip(a.hotkeyErr)
				}
				if status == "" && a.hotkeyErr == "" {
					ui.Text(c, "修改后点保存设置生效").FontSize(11).TextColor(t.TextMuted)
				}
			})
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
