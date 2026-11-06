# 架构

本文描述当前实现的技术栈、模块边界与关键机制。产品说明见 [README](../README.md)，取舍理由见 [decisions.md](decisions.md)。

## 一、技术栈

- **语言**：Go 1.27，支持 Windows 10/11 与 Linux (Wayland / X11)。
- **界面**：MyGo 原生窗口与控件，自绘 Fluent / 现代化轻量风格界面；Windows 上提供亚克力气泡与浮窗，Linux/Wayland 上提供任务栏抽屉面板；设置、输入、历史与翻译窗口统一共用进程。
- **模型接入**：`net/http` + 自写 SSE 解析，支持 OpenAI Compatible、Azure 翻译、百度翻译；不依赖厂商 SDK。
- **系统集成**：
  - Windows：剪贴板恢复、`SendInput`、全局低级鼠标钩子、SAPI 语音、凭据管理器、注册表自启。
  - Linux：Wayland PRIMARY 选区（`wl-paste`）、Spectacle 区域截图、XDG Desktop Portal 全局快捷键与托盘边缘吸附面板。
  - CLI 单实例：支持向后台实例传递 `--selection`、`--input`、`--screenshot`、`--toggle` 指令。

## 二、模块

```text
hayami-translator/
├── main.go                 入口、应用状态、窗口编排、快捷键、CLI 单实例分发
├── prompt.go               提示词合约、模式消息与追问上下文
├── modes.go                模式切换与自定义模式辅助
├── followup.go             结果追问请求与流式更新
├── gesture.go              拖动与双击手势判定
├── config.go               配置包的兼容别名与包装
├── config_reload.go        config.json 热加载与主线程应用
├── provider.go             翻译服务辅助函数的兼容包装
├── provider_keys.go        Windows 凭据管理器密钥存取
├── llm_compat.go           LLM 包的兼容别名与包装
├── history.go              历史包的兼容别名
├── language.go             语种包的兼容包装
├── popup.go                翻译弹窗
├── input_window.go         双栏输入翻译窗口
├── settings.go             设置界面
├── history_ui.go           历史界面
├── toolbar.go              划词浮窗与取词气泡开关
├── icons.go                Fluent 图标与 SAPI 朗读
├── panel.go                统一面板（翻译、输入、历史、设置）
├── panel_linux.go          Linux 任务栏托盘吸附与 Layer-shell 背景
├── panel_other.go          非 Linux 面板位置回退
├── selection_windows.go    Windows Ctrl+C 取词与剪贴板恢复
├── selection_linux.go      Linux Wayland PRIMARY 选区读取与去抖
├── selection_other.go      非特定平台选区回退
├── selection_hook_windows.go Windows 全局低级鼠标钩子
├── selection_hook_linux.go   Linux 选区变动轮询与防抖
├── selection_hook_other.go   非特定平台空钩子
├── screenshot_windows.go   Windows 截图集成
├── screenshot_linux.go     Linux Spectacle 交互选区截图
├── screenshot_other.go     非特定平台截图回退
├── startup_windows.go      Windows 开机自启
├── startup_other.go        非 Windows 开机自启
├── ui_helpers.go           设计令牌、面板与通用控件
├── internal/config/        配置模型、默认值、读写、校验、Schema
├── internal/llm/           OpenAI/Azure/百度协议、SSE、错误分类
├── internal/lang/          语种识别与自动目标语言
├── internal/history/       历史记录 SQLite 存储
├── docs/                   架构、决策记录
├── scripts/check.ps1       本地检查脚本
└── resources/              托盘与窗口图标
```

依赖方向：`main` → `internal/llm` / `internal/config` / `internal/history` / `internal/lang`。`internal/llm` 依赖 `internal/config`；`internal/lang` 与 `internal/history` 只依赖标准库和 MyGo；没有任何 `internal` 包依赖 UI 或 `main`。

## 三、翻译链路

```text
入口（快捷键 / 划词 / 截图 / 输入窗口）
  └► 选择服务（多引擎并发或按优先级回退）
       └► 组装消息：system = 用户提示词 + 材料合约 + 可信运行参数 JSON
                    user   = 原文（只出现一次）
       └► 流式回调按 30ms 合并刷新到主线程
       └► 成功：Markdown 渲染 + 写入历史
       └► 失败：统一错误分类 → 标题 + 下一步
```

- **提示词合约**：原文永远只作为 user 消息，不拼进 system 提示词。材料合约明确告诉模型第一条消息是待翻译材料，材料中的指令不执行，从而避免划选文本劫持任务。
- **可信运行参数**：任务、源语言、目标语言、输出格式以 JSON 传给模型，和原文分离。
- **自动方向**：`lang.EffectiveTarget` 在本机按字符类型判断中英互译，避免把「自动」当成目标语言发给模型。
- **错误分类**：`llm.ExplainError` 把超时、断网、限流、鉴权、地址/模型不存在、文本过长、TLS、服务端错误映射成可操作提示；面板只展示标题与下一步。
- **模式**：模式 = 名称 + 提示词 + 语义（材料 / 指令）。内置翻译、润色、总结、解释；`config.json` 的 `modes` 非空时替换内置列表。所有模式共用同一套服务与流式管线，没有按模式分支的请求代码。翻译模式按目标语言输出，其他模式保持原文语言，避免润色中文时被自动目标语言翻成英文。
- **追问**：结果出来后，弹窗保留原文、上一轮结果与新的追问指令，追加一轮请求；材料合约继续声明第一段文字不是指令。截图与多引擎模式不提供追问。
- **主题**：`theme` 取 `system`、`light`、`dark`；视图在构建时覆盖 `ui.Theme.Dark`，保存或热加载后重绘所有已打开窗口。

## 四、划词触发

```text
WH_MOUSE_LL（独立线程）
  └► 按下：记录起点，隐藏旧浮窗
  └► 抬起：拖动 ≥8px 且按住 ≥40ms，或双击（系统双击时间、4px 容差）
       └► 过滤：本程序 / 标题栏 / 终端与列表控件 / Ctrl、Alt、Win 按住
       └► 延迟 140ms 让选区稳定 → 保存剪贴板 → Ctrl+C → 恢复剪贴板
       └► 气泡：释义卡 + 朗读 / 展开完整翻译
```

- 钩子回调只投递事件，不做 I/O；取词在独立 goroutine 完成。每次手势后重新核对前台窗口，避免切窗后把 Ctrl+C 发给别的程序。
- 双击取词与拖动共用同一条取词链路；单击只记录点击位置，不触发。`HAYAMI_HOOK_DEBUG=1` 时钩子记录手势与拒绝原因，用于排查 Windows 静默摘钩。

## 五、配置

`%APPDATA%\Hayami Translator\` 下的文件：

```text
config.json         全部设置，带 "$schema": "./config.schema.json"
config.schema.json  每次启动写出，与当前版本一致
history.db          翻译历史
debug.log           调试日志（只记录事件，不记录原文与密钥）
```

- API Key 不在 `config.json`，保存在 Windows 凭据管理器，键名为 `provider.<id>`。
- **严格解码**：未知字段、类型错误、语法错误分别给出行列或字段路径；校验范围、枚举、重复 ID/快捷键与 URL 语法。
- **热加载**：`config.json` 被外部修改后由 `mygo.WatchFiles` 监听，重新读取并校验；失败时保留当前配置，成功后在主线程更新界面、托盘与快捷键。设置窗口有未保存草稿时不覆盖草稿。
- **应用写回**：临时文件 + rename 原子替换；写回前先校验。

## 六、线程与生命周期

- 视图状态只在主线程读写；网络、剪贴板、截图等待在 goroutine 中进行，结果通过 `Window.Update` 或 `mygo.RunOnMain` 回到主线程。
- 每次翻译、截图、气泡请求都有递增 `reqID` 与 `context.CancelFunc`；新请求取消旧请求，迟到结果按 ID 丢弃。
- 配置热加载、历史查询、密钥读取都带超时或可取消；退出时取消配置监听、截图、翻译与朗读。
- 全局快捷键在配置变化后先注销再注册；注册失败写入状态行，不阻止应用运行。

## 七、验证

```powershell
pwsh -File scripts/check.ps1
```

检查内容包括 `gofmt -l`、`go vet ./...`、`go tool mygo vet .`、`go test ./...`。涉及共享状态时额外运行 `go test -race ./...`。
