# Hayami Translator

<p align="center">
  <strong>极简、现代、高响应速度的 Windows 原生划词与快捷键翻译工具</strong>
</p>

<p align="center">
  基于 Go 与 <a href="https://github.com/egoist/mygo">MyGo</a> 构建 · 拒绝 Electron 臃肿包体 · 极低系统资源占用
</p>

---

## ✨ 核心特性

- 🖱️ **划词悬浮工具条**：鼠标选中文本后自动轻量浮显快捷图标，点击即刻弹出翻译，支持在设置中一键开启或关闭。
- ⌨️ **全局快捷键呼出**：
  - `Alt+Shift+D`：快速呼出主翻译窗口。
  - `Alt+Shift+S`：选区快速翻译快捷键。
- 🤖 **多引擎翻译支持**：
  - **OpenAI 兼容协议**：支持 OpenAI (GPT-4o / GPT-4o-mini)、DeepSeek、Moonshot (Kimi)、通义千问 (Qwen)、Ollama 本地大模型及任意自定义兼容网关。
  - **Azure 翻译**：集成微软 Azure Cognitive Services 翻译服务。
  - **百度翻译**：集成百度翻译开放平台通用翻译 API。
- 🔀 **多引擎同时翻译对比与智能回退**：
  - **同时翻译模式**：支持一键开启多引擎并发翻译，并行向所有已启用的服务发起请求，卡片式并列展示各引擎译文，支持独立复制或一键复制全部。
  - **优先级回退模式**：按优先级顺序尝试首选引擎，异常时自动平滑回退至备选服务。
- 🔒 **凭据安全存储**：API Key 等敏感凭据直接交由 Windows 凭据管理器（Credential Store）加密保管，不随本地配置文件明文落盘。
- 🕒 **翻译历史管理**：本地记录翻译历史，支持按原文/译文关键词快速筛选检索、一键复制及历史清理。
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
├── history.go                  # 历史记录内存/持久化存储实现
├── history_ui.go               # 翻译历史记录查看与搜索窗口
├── settings.go                 # 设置界面（引擎增删改查、快捷键、提示词配置）
├── popup.go                    # 翻译悬浮弹窗 UI
├── toolbar.go                  # 鼠标划词就近浮动的迷你工具条 UI
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
