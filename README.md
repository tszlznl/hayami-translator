# Hayami Translator

<p align="center">
  <strong>极简、现代、高响应速度的跨平台原生划词与快捷键翻译工具（Windows 10/11 & Linux Wayland/X11）</strong>
</p>

<p align="center">
  基于 Go 与 <a href="https://github.com/egoist/mygo">MyGo</a> 构建 · 拒绝 Electron 臃肿包体 · 极低系统资源占用
</p>

---

## ✨ 核心特性

- 🎨 **原生轻量统一面板**：自适应桌面风格（Windows 11 Fluent / KDE Plasma Breeze），支持跟随系统、浅色与深色主题；翻译、双栏输入、历史与设置整合在同一常驻进程与快速面板中。
- 🖱️ **无打扰划词自动翻译**：
  - **Windows**：全局低级鼠标钩子（`WH_MOUSE_LL`）支持拖动与双击取词，通过剪贴板序列号校验与无损恢复避免污染剪贴板。
  - **Linux**：Wayland 下通过 `wl-paste --primary --watch` 事件驱动监听 PRIMARY 选区（零空转轮询、零剪贴板污染），X11 下自动回退 `xclip` / `xsel`。
- ⌨️ **全局快捷键与 CLI 单实例联动**：
  - `Alt+F` / `hayami -s`：快速翻译当前选中文本；若无选区则直接进入输入模式。
  - `Alt+D` / `hayami -i`：打开双栏输入翻译页（支持全语种互换与实时流式输出）。
  - `Alt+S` / `hayami -c`：区域截图翻译（Windows 调系统截图；Linux 支持 Spectacle、grim+slurp、gnome-screenshot、Flameshot、xfce4-screenshooter、scrot 自动回退）。
  - `hayami -t`：显示/隐藏面板；`hayami -p`：打开设置；`hayami -b`：后台静默驻留托盘。
- ✨ **多模式提示词与上下文追问**：内置「翻译 / 润色 / 总结 / 解释」四种模式并支持完全自定义，支持对译文继续追问微调。
- 🔊 **本地语音朗读 (TTS)**：Windows 通过 SAPI（经 stdin 安全传参且隐藏控制台窗口）、Linux 通过 `espeak-ng` 朗读，支持随时打断与重播。
- 🤖 **多引擎并发对比与优先级回退**：
  - 支持 **OpenAI Compatible**（OpenAI / DeepSeek / Kimi / Qwen / Ollama / 本地网关等，支持视觉截图与温度/MaxTokens 调节）、**Azure 翻译**、**百度翻译**。
  - 支持多引擎并发并列展示或按优先级自动回退；非翻译模式与追问自动仅使用 LLM 服务。
- 🔒 **凭据安全存储**：API Key 等敏感凭据保存于系统密钥服务（Windows 凭据管理器 / Linux Secret Service），绝不写入明文配置文件。
- 🕒 **SQLite 本地翻译历史**：支持安全关键词搜索、一键回填复用（不篡改全局默认语种/模式）及带真实总条数提示的历史清空。
- 🚀 **开机静默自启**：支持 Windows 注册表与 Linux XDG Autostart（含 KDE Plasma 静默启动元数据）。

---

## 🏗️ 架构与技术栈

- **主语言**：Go 1.27+
- **GUI 框架**：[MyGo](https://github.com/egoist/mygo)（原生窗口、系统托盘、全局快捷键与自绘控件）
- **更多细节**：架构与模块边界见 [docs/architecture.md](docs/architecture.md)，关键设计取舍见 [docs/decisions.md](docs/decisions.md)。

---

## 📂 代码组织

```text
hayami-translator/
├── main.go                       # 程序入口、托盘、快捷键、CLI 单实例与主翻译调度
├── panel.go / panel_linux.go     # 统一面板、多显示器热插拔遮罩与托盘吸附布局
├── popup.go                      # 翻译结果视图、智能滚动与流式 Markdown 渲染
├── input_window.go               # 双栏输入翻译视图与语种互换
├── settings.go                   # 侧边栏设置界面（引擎、模型参数、快捷键、模式、主题）
├── history_ui.go                 # 历史记录搜索、展示与清空确认
├── followup.go / modes.go / prompt.go # 模式管理、材料/指令隔离提示词与追问链路
├── config_reload.go / provider_keys.go / migrate.go # 配置热加载、系统密钥存取与迁移
├── icons.go / speech_*.go        # Fluent/Breeze 矢量图标与跨平台安全 TTS
├── selection_*.go / selection_hook_*.go # Windows/Linux 选区读取与划词监听
├── screenshot_*.go               # Windows/Linux 区域截图集成
├── startup_*.go                  # Windows/Linux 开机自启集成
├── ui_helpers.go                 # 主题配色（Fluent / KDE Breeze）与通用控件
├── internal/
│   ├── config/                   # 配置模型、默认值、严格解码、校验与 JSON Schema
│   ├── llm/                      # OpenAI / Azure / 百度协议、SSE 流式解析与错误分类
│   ├── lang/                     # 语种检测与中英自动方向
│   └── history/                  # 并发安全的 SQLite 历史存储
├── scripts/                      # check.ps1 / check.sh 本地检查脚本
└── resources/                    # 应用托盘与窗口图标资源
```

---

## 🛠️ 本地开发与构建

### 前置要求

- **操作系统**：Windows 10 / 11 (64-bit) 或 Linux (Wayland / X11)
- **Go 环境**：Go 1.27+（模块已集成 `go tool mygo`）
- **Linux 可选系统工具**：`wl-paste`（Wayland）或 `xclip`/`xsel`（X11）、`spectacle`/`grim`+`slurp`/`flameshot` 等截图工具、`espeak-ng`（语音朗读）

### 常用命令

```bash
# 1. 运行本地全套检查（gofmt + go vet + mygo vet + go test）
bash scripts/check.sh          # Linux / macOS（加 --race 启用竞态检测）
# pwsh -File scripts/check.ps1 # Windows（加 -Race 启用竞态检测）

# 2. 开发模式启动
go tool mygo dev

# 3. 生产打包构建
go tool mygo build
```

打包产物将输出在 `build/` 目录下。

---

## 📄 开源许可

本项目遵循 MIT 许可证。
