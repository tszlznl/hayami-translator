# Hayami Translator

<p align="center">
  <strong>极简、现代、高响应速度的 Windows 原生划词与快捷键翻译工具</strong>
</p>

<p align="center">
  基于 Go 与 <a href="https://github.com/egoist/mygo">MyGo</a> 构建 · 拒绝 Electron 臃肿包体 · 极低系统资源占用
</p>

---

## ✨ 核心特性

- 🎨 **Windows 11 Fluent 原生体验**：精细还原 Win11 官方设计规范，适配浅色与深色主题，采用 Segoe UI Variable 字体、精美圆角、柔和光晕阴影与主题强调色。
- 🖱️ **灵活的划词即译与气泡**：
  - **显示图标**：选中文本后在光标旁轻量浮显快捷图标，点击即刻弹出翻译。
  - **取词即译气泡**：选词即显精简释义气泡，包含音标、词性与简明释义，支持一键展开完整翻译。
  - **直接弹出**：划选完成后就近自动展开完整翻译浮窗。
- ⌨️ **全局快捷键呼出**：
  - `Alt+F`：快速翻译当前选中文本；若未选中文本则呼出主翻译窗口。
  - `Alt+D`：独立双栏输入翻译窗口（支持自适应双栏、源/目标语种切换与实时流式输出）。
  - `Alt+S`：区域截图快速翻译（调用系统截屏框选屏幕，使用视觉模型识别并翻译）。
- 💻 **命令行调用与桌面快捷键联动**：
  - 支持向运行中实例发送指令：`hayami -s`（划选翻译）、`hayami -i`（输入翻译）、`hayami -c`（截图翻译）、`hayami -t`（显示/隐藏面板）。方便在 Linux/Wayland（如 KDE 系统设置）中自由绑定任意系统快捷键。
- 🔊 **Windows SAPI 语音朗读 (TTS)**：内置原生语音发音支持，点击朗读按钮手动发音，并支持随时打断与重播。
- 🤖 **多引擎翻译支持**：
  - **OpenAI 兼容协议**：支持 OpenAI (GPT-4o / GPT-4o-mini)、DeepSeek、Moonshot (Kimi)、通义千问 (Qwen)、Ollama 本地大模型及任意自定义兼容网关。
  - **Azure 翻译**：集成微软 Azure Cognitive Services 翻译服务。
  - **百度翻译**：集成百度翻译开放平台通用翻译 API。
- 🔀 **多引擎同时翻译对比与智能回退**：
  - **同时翻译模式**：支持一键开启多引擎并发翻译，并行向所有已启用的服务发起请求，卡片式并列展示各引擎译文，支持独立复制或一键复制全部。
  - **优先级回退模式**：按优先级顺序尝试首选引擎，异常时自动平滑回退至备选服务。
- 🔒 **凭据安全存储**：API Key 等敏感凭据直接交由 Windows 凭据管理器（Credential Store）加密保管，不随本地配置文件明文落盘。
- 🕒 **翻译历史管理**：本地记录翻译历史，支持按原文/译文关键词快速筛选检索、点击重新打开复用、及一键清空历史。
- 🌐 **双向智能语种识别**：智能检测源语言与目标语言，支持中英自动互译与自定义目标语种。
- 🚀 **开机自启集成**：深度集成 Windows 启动项注册表，无缝开机后台静默自启并驻留系统托盘。

---

## 🏗️ 架构与技术栈

- **主语言**：Go 1.27+
- **GUI 框架**：[MyGo](https://github.com/egoist/mygo) (Windows 原生窗口与系统控件)
- **底层集成**：
  - Windows 原生剪贴板与模拟按键处理
  - Windows 全局低级鼠标钩子 (`WH_MOUSE_LL`) 精准监测选词划动
  - Windows 凭据管理器 (`wincred`) 安全保管 API Key
  - Windows 注册表 (`HKCU\Software\Microsoft\Windows\CurrentVersion\Run`) 管理自启
  - Windows SAPI (`System.Speech.Synthesis`) 语音朗读集成

---

## 📂 代码组织

```text
hayami-translator/
├── main.go                     # 程序入口、托盘图标、快捷键与主事件调度
├── config.go                   # 配置模型定义、读写与迁移逻辑
├── provider.go                 # 翻译引擎统一抽象接口与通用定义
├── provider_keys.go            # Windows 凭据管理器密钥存取实现
├── translate_providers.go      # OpenAI / Azure / 百度翻译的具体协议实现
├── llm.go                      # OpenAI 兼容接口流式与非流式调用实现
├── language.go                 # 语种检测与中英自动互译逻辑
├── dict.go                     # 快速取词释义与轻量内置离线词典
├── icons.go                    # Windows 11 Fluent 线性图标与 Windows SAPI TTS
├── history.go                  # 历史记录内存/持久化存储实现
├── history_ui.go               # 翻译历史记录查看与搜索窗口
├── settings.go                 # Windows 11 侧边栏式设置界面
├── popup.go                    # 翻译悬浮弹窗 UI（圆角、双模式与响应式排版）
├── input_window.go             # 独立双栏输入翻译窗口（响应式布局）
├── toolbar.go                  # 鼠标划词迷你悬浮按钮与取词即译气泡 UI
├── selection_windows.go        # Windows 剪贴板读取与文本获取实现
├── selection_hook_windows.go   # Windows 全局鼠标与键盘监听钩子
├── startup_windows.go          # Windows 开机自启注册表读写
├── ui_helpers.go               # 通用 UI 组件与样式辅助函数
└── resources/                  # 应用托盘与窗口图标资源
```

---

## 🛠️ 本地开发与构建

### 前置要求

- 操作系统：Windows 10 / Windows 11 (64-bit)
- Go 环境：Go 1.27+（已自带 `mygo` 工具链配置）

### 常用命令

```powershell
# 1. 运行所有单元测试
go test ./...

# 2. 执行 MyGo 静态生命周期与原生 UI 检查
go tool mygo vet

# 3. 检查本地开发环境工具链就绪度
go tool mygo doctor

# 4. 以开发模式启动（支持热重载）
go tool mygo dev

# 5. 生产打包构建
go tool mygo build
```

打包产物将输出在 `build/` 目录下。

---

## 📄 开源许可

本项目遵循 MIT 许可证。
