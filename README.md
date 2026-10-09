# stai — SourceTree 的 AI 伴侣工具

stai 是一个独立的命令行工具，通过 Git 标准机制（钩子、自定义操作）接入 SourceTree 的工作流，
不是 SourceTree 插件（SourceTree 没有插件机制，原因见 [docs/design.md](docs/design.md)）。

提供三类能力：

1. **commit 信息生成** —— 读暂存区 diff，按 Conventional Commits 生成提交信息（已可用）；
2. **提交前 code review** —— 对暂存区做 AI 审查，输出问题清单（规划中，M2）；
3. **冲突合并协助** —— 接管 mergetool，逐冲突块给出解释与推荐解法（规划中，M3）。

## 状态

| 功能 | 命令 | 状态 |
| --- | --- | --- |
| commit 信息生成 | `stai gen`、`stai hook prepare-commit-msg`、`stai install` | ✅ 可用（M1） |
| 提交前 review | `stai review` | 🚧 未实现（M2），目前仅打印提示 |
| 冲突合并协助 | `stai mergetool` | 🚧 未实现（M3），目前仅打印提示 |

配置项 `[review] strict` 目前不生效，待 M2 实现后启用。

## 前置条件

- macOS（SourceTree 集成依赖 macOS 的 `osascript`、`pbcopy` 与 SourceTree 的 `actions.plist`）；
- SourceTree（已在 4.2.19 上验证）；
- Go 1.23+（仅从源码构建时需要）；
- 一个 OpenAI 兼容的模型服务，默认为本机 Ollama（`http://localhost:11434/v1`），也可指向 LM Studio 或网关。

## 安装

在仓库根目录执行（全局安装后，任意仓库都可以运行）：

```bash
stai install                  # 写 prepare-commit-msg 钩子 + 注册 SourceTree 自定义操作
stai install -no-sourcetree   # 只装钩子，不动 SourceTree
```

`install` 做两件事：

1. 向当前仓库写入 `prepare-commit-msg` 钩子（一段调用 stai 二进制的脚本）；
2. 向 SourceTree 的自定义操作存储写入条目
   `~/Library/Application Support/SourceTree/actions.plist`，菜单项为 **AI 生成提交信息**，
   参数为 `gen $REPO`（SourceTree 会把 `$REPO` 展开为仓库路径），快捷键 **⌥G**
   （可在 SourceTree 设置 → 自定义操作中修改）。

该操作是幂等的：重复执行会替换指向本二进制或同名的旧条目，其他工具创建的条目原样保留。

> **注意**：重装前先退出 SourceTree。它在运行时会用内存中的动作列表覆盖 `actions.plist`，
> 导致写入丢失。注册后需重启 SourceTree 才会出现菜单项。

## 使用方式

### 自定义操作（推荐）

在 SourceTree 中暂存改动，然后执行 **动作 → 自定义操作 → AI 生成提交信息**（或按 ⌥G）：

1. 消息复制到剪贴板，并弹出系统通知展示全文（SourceTree 会丢弃自定义操作的输出，不看通知就看不到消息）；
2. 光标点在提交框中，按 Cmd+V 粘贴；
3. 在提交框中审阅、修改后提交。

全程不改变窗口焦点，不会未经确认就写入提交框，也不会覆盖你已写的草稿。

等价命令行：`stai gen`；`stai gen --edit` 会先弹出可编辑对话框，确认后再复制。

### 钩子兜底（零操作）

直接提交且提交框留空时，`prepare-commit-msg` 钩子会自动生成信息并填入提交框。

以下情况不会处理：merge / squash / `commit --amend` 类提交，以及已有信息的提交。
生成失败只打印警告，不会阻断提交（内部 120 秒超时保护）。

临时禁用钩子（不卸载）：`STAI_DISABLE=1 git commit ...`

## 配置

配置按以下顺序合并，后者覆盖前者：

1. 内置默认值
2. 全局配置 `~/.config/stai/config.toml`
3. 仓库配置 `.stai.toml`（位于仓库根目录，可提交进库，用于团队共享约定）
4. 环境变量（最高优先级，可用于临时覆盖）

全局配置 `~/.config/stai/config.toml` 中的每一项都有默认值，未写出的项使用下表的默认值。
可以直接复制以下示例作为起点：

```toml
[provider]
base_url        = "http://localhost:11434/v1"
api_key         = ""
model           = "qwen2.5-coder:7b"
timeout_seconds = 120

[commit]
style            = "conventional"
language         = "zh-CN"
types            = ["feat", "fix", "refactor", "docs", "chore", "test", "style", "perf", "build", "ci"]
subject_max      = 50
body_min_lines   = 100
body_min_files   = 3
body_max_items   = 5
max_diff_chars   = 60000
retries          = 1

[review]
strict = false

[hook]
timeout_seconds = 120

[notify]
title    = "stai"
subtitle = "提交信息已复制到剪贴板，Cmd+V 粘贴到提交框"

[sourcetree]
action_caption     = "AI 生成提交信息"
shortcut_key_code  = 5
shortcut_modifiers = 524288
shortcut_display   = "⌥G"

[log]
path = "~/Library/Logs/stai.log"
```

各项说明：

| 配置项 | 默认值 | 说明 |
| --- | --- | --- |
| `provider.base_url` | `http://localhost:11434/v1` | OpenAI 兼容接口地址（Ollama、LM Studio、网关均可） |
| `provider.api_key` | `""` | 接口密钥，本地模型留空 |
| `provider.model` | `qwen2.5-coder:7b` | 模型名称 |
| `provider.timeout_seconds` | `120` | 单次模型请求超时（秒） |
| `commit.style` | `conventional` | 目前只支持 `conventional`，其他值会报错 |
| `commit.language` | `zh-CN` | 提交信息语言，`zh-CN` 或 `en` |
| `commit.types` | `feat` … `ci`（共 10 种） | 允许的 type；不在列表中的输出会被重试，仍不合规则报错 |
| `commit.subject_max` | `50` | 概要字数上限（提示给模型） |
| `commit.body_min_lines` | `100` | 改动行数超过该值时才写 body |
| `commit.body_min_files` | `3` | 涉及文件数超过该值时才写 body |
| `commit.body_max_items` | `5` | body 最多条目数 |
| `commit.max_diff_chars` | `60000` | 超过该长度的 diff 会被截断后再发送 |
| `commit.retries` | `1` | 输出不合规时的重试次数 |
| `review.strict` | `false` | M2 实现后生效：`true` 时 pre-commit 发现高危问题才阻断 |
| `hook.timeout_seconds` | `120` | `prepare-commit-msg` 钩子的总超时（秒） |
| `notify.title` | `stai` | 系统通知标题 |
| `notify.subtitle` | `提交信息已复制到剪贴板，Cmd+V 粘贴到提交框` | 系统通知副标题 |
| `sourcetree.action_caption` | `AI 生成提交信息` | SourceTree 自定义操作的菜单名，修改后需重新执行 `stai install` |
| `sourcetree.shortcut_key_code` | `5` | 快捷键键码，`5` 为 G |
| `sourcetree.shortcut_modifiers` | `524288` | 修饰键，`524288` 为 Option (⌥) |
| `sourcetree.shortcut_display` | `⌥G` | 快捷键显示文本 |
| `log.path` | `~/Library/Logs/stai.log` | 诊断日志路径，记录每次 `gen` 的参数、消息和剪贴板结果 |

仓库级 `.stai.toml` 使用相同结构，只写需要覆盖的字段即可。

### 环境变量

| 变量 | 作用 |
| --- | --- |
| `STAI_BASE_URL` | 覆盖 `provider.base_url` |
| `STAI_API_KEY` | 覆盖 `provider.api_key` |
| `STAI_MODEL` | 覆盖 `provider.model` |
| `STAI_DISABLE` | 非空时钩子直接放行，不生成信息 |


## 开发

```bash
go build ./...                 # 构建
go test ./...                  # 测试
go run ./cmd/stai help         # 查看命令帮助
```

## 目录结构

```
cmd/stai/        CLI 入口与子命令（gen / hook / review / mergetool / install）
internal/ai      OpenAI 兼容接口客户端与提交信息生成
internal/config 分层配置加载
internal/git     git 命令封装（暂存区 diff、钩子路径）
docs/            设计说明（design.md）
```

## 路线图

- **M1 — commit 信息生成**（已完成）：`stai gen`（生成并复制）、`stai hook prepare-commit-msg`（空信息兜底）、`stai install`。
  验收标准：在 SourceTree 中全流程跑通，不离开 GUI。
- **M2 — 提交前 review**：`stai review`（自定义操作，建议模式）+ pre-commit 严格模式开关。
- **M3 — 冲突合并协助**：`stai mergetool` 接入 `git config mergetool`，
  逐冲突块提供「解释 + 推荐 + 采纳/自行修改」的交互界面。
