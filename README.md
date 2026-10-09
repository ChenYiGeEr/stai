# stai — SourceTree 的 AI 伴侣工具

stai 是一个独立的命令行工具，通过 Git 标准机制（钩子、自定义操作）接入 SourceTree 的工作流，
不是 SourceTree 插件（SourceTree 没有插件机制，原因见 [docs/design.md](docs/design.md)）。

提供三类能力：

1. **commit 信息生成** —— 读暂存区 diff，按 Conventional Commits 生成提交信息（已可用）；
2. **提交前 code review** —— 对暂存区做 AI 审查，输出问题清单，可选严格模式阻断提交（已可用）；
3. **冲突合并协助** —— 接管 mergetool，逐冲突块给出解释与推荐解法（规划中，M3）。

## 状态

| 功能 | 命令 | 状态 |
| --- | --- | --- |
| commit 信息生成 | `stai gen`、`stai hook prepare-commit-msg`、`stai install` | ✅ 可用（M1） |
| 提交前 review | `stai review`、`stai hook pre-commit`、`stai install` | ✅ 可用（M2） |
| 冲突合并协助 | `stai mergetool` | 🚧 未实现（M3），目前仅打印提示 |

## 前置条件

- macOS（SourceTree 集成依赖 macOS 的 `osascript`、`pbcopy` 与 SourceTree 的 `actions.plist`）；
- SourceTree（已在 4.2.19 上验证）；
- Go 1.23+（仅从源码构建时需要）；
- 一个 OpenAI 兼容的模型服务，默认为本机 Ollama（`http://localhost:11434/v1`），也可指向 LM Studio 或网关。

## 安装

在仓库根目录执行（全局安装后，任意仓库都可以运行）：

```bash
stai install                  # 写入两个钩子 + 注册两个 SourceTree 自定义操作
stai install -no-sourcetree   # 只装钩子，不动 SourceTree
stai uninstall                # 移除 install 写入的一切（只删 stai 自己写的）
```

`install` 做三件事：

1. 向当前仓库写入 `prepare-commit-msg` 钩子（空提交信息时自动生成，绝不阻断提交）；
2. 向当前仓库写入 `pre-commit` 钩子（仅在 `review.strict = true` 时审查并阻断高危提交，
   其余情况直接放行）；
3. 向 SourceTree 的自定义操作存储写入两条条目
   `~/Library/Application Support/SourceTree/actions.plist`：
   - **AI 生成提交信息**，参数 `gen $REPO`，快捷键 **⌥G**；
   - **AI 审查改动**，参数 `review $REPO`，快捷键 **⌥R**。

   （`$REPO` 由 SourceTree 展开为仓库路径；快捷键可在 SourceTree 设置 → 自定义操作中修改。）

`install` 与 `uninstall` 都是幂等的：install 替换指向本二进制或同名的旧条目；
uninstall 只删除带 `installed by stai` 标记的钩子和匹配 stai 的动作条目，
其他工具创建的内容原样保留。

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

### 提交前审查（M2）

在 SourceTree 中暂存改动，执行 **动作 → 自定义操作 → AI 审查改动**（或按 ⌥R）：

- 审查报告直接打印（命令行）或通过系统通知展示（SourceTree）；
- 变更行数超过 `review.group_max_lines`（默认 100）时，diff 按文件分组、
  逐组串行调用模型审查后合并排序（high → low）。小模型面对整份大 diff 会
  稀释注意力漏报问题，逐文件审查能显著缓解（借鉴 alibaba/open-code-review
  的分组设计）；
- 审查提示词内置两条核心原则（精确优先于召回、不报 gofmt/go vet 能查的
  静态问题）和一份精简 Go 专项清单（diff 含 `.go` 文件时注入），
  `review.rules` 可追加项目自定义规则；
- 问题条数超过 `review.notify_max_findings`（默认 5）时，通知只显示各严重度
  （high / medium / low）的摘要，完整报告写入 `review.report_path`
  （默认 `.git/stai-review.md`），通知中附有文件路径；
- 无问题时通知显示「OK 未发现问题」；
- review 可用 `review.base_url` / `review.model` 单独指定更强的模型
  （gen 继续用本地快模型），见配置表。

等价命令行：`stai review`；`stai review -strict` 本次运行按严格模式处理
（发现高危问题退出码为 1，可用于脚本）。

#### 严格模式（pre-commit 阻断）

`review.strict = true`（全局或仓库配置）时，每次 `git commit` 前都会审查暂存区：

- 发现 **high** 级问题 → 提交被阻断，报告显示在提交输出里；
- 无高危问题、模型调用失败或输出无法解析 → 一律放行（AI 故障绝不阻断提交）；
- 绕过方式：`STAI_DISABLE=1 git commit ...` 或 `git commit --no-verify`。

默认关闭。建议先用建议模式（⌥R / `stai review`）观察一段时间，确认模型在你
的项目上误报率可接受后再开启。

> 提示：审查质量取决于模型能力。建议 gen 用本地小模型（秒级），review 用
> `review.model` 指向更强的模型；纯本地小模型时把 `review.group_max_lines`
> 调小（如 50）可进一步提高召回，代价是串行调用次数变多、耗时变长。

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
notify_max_findings = 5
report_path = ".git/stai-review.md"
group_max_lines = 100
# rules = ["所有新函数的错误必须 logf 或向上返回"]  # 项目附加审查规则
# base_url = "" / api_key = "" / model = ""        # review 专用模型，留空继承 [provider]
# temperature = 1                                  # 模型只接受 temperature=1 时设置（如部分推理模型），缺省继承 [provider]

[hook]
timeout_seconds = 120

[notify]
title    = "stai"
subtitle = "提交信息已复制到剪贴板，Cmd+V 粘贴到提交框"

[sourcetree]
action_caption            = "AI 生成提交信息"
shortcut_key_code         = 5
shortcut_modifiers        = 524288
shortcut_display          = "⌥G"
review_action_caption     = "AI 审查改动"
review_shortcut_key_code  = 15
review_shortcut_modifiers = 524288
review_shortcut_display   = "⌥R"

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
| `provider.temperature` | `0` | 请求 temperature，`0` 为确定性输出；个别模型只接受 `1` |
| `commit.style` | `conventional` | 目前只支持 `conventional`，其他值会报错 |
| `commit.language` | `zh-CN` | 提交信息语言，`zh-CN` 或 `en` |
| `commit.types` | `feat` … `ci`（共 10 种） | 允许的 type；不在列表中的输出会被重试，仍不合规则报错 |
| `commit.subject_max` | `50` | 概要字数上限（提示给模型） |
| `commit.body_min_lines` | `100` | 改动行数超过该值时才写 body |
| `commit.body_min_files` | `3` | 涉及文件数超过该值时才写 body |
| `commit.body_max_items` | `5` | body 最多条目数 |
| `commit.max_diff_chars` | `60000` | 超过该长度的 diff 会被截断后再发送 |
| `commit.retries` | `1` | 输出不合规时的重试次数 |
| `review.strict` | `false` | `true` 时 pre-commit 发现高危问题才阻断（详见「严格模式」） |
| `review.notify_max_findings` | `5` | 问题条数超过该值时，通知只给摘要，全文写入报告文件 |
| `review.report_path` | `.git/stai-review.md` | 完整报告文件路径（相对仓库根目录） |
| `review.group_max_lines` | `100` | 变更行数超过该值时按文件分组、逐组串行审查后合并（借鉴 open-code-review 的分组阈值） |
| `review.rules` | `[]` | 项目附加审查规则，原样注入审查提示词 |
| `review.base_url` / `review.api_key` / `review.model` | 继承 `[provider]` | review 专用模型覆盖，留空继承全局；可让 review 用大模型、gen 用本地快模型 |
| `review.temperature` | 继承 `provider.temperature` | 仅当 review 模型对 temperature 有限制时设置（如只接受 `1` 的推理模型） |
| `hook.timeout_seconds` | `120` | 钩子的总超时（秒），对 `prepare-commit-msg` 和 `pre-commit` 都生效 |
| `notify.title` | `stai` | 系统通知标题 |
| `notify.subtitle` | `提交信息已复制到剪贴板，Cmd+V 粘贴到提交框` | 生成提交信息的通知副标题 |
| `sourcetree.action_caption` | `AI 生成提交信息` | 生成提交信息动作的菜单名，修改后需重新执行 `stai install` |
| `sourcetree.shortcut_key_code` | `5` | 快捷键键码，`5` 为 G |
| `sourcetree.shortcut_modifiers` | `524288` | 修饰键，`524288` 为 Option (⌥) |
| `sourcetree.shortcut_display` | `⌥G` | 快捷键显示文本 |
| `sourcetree.review_action_caption` | `AI 审查改动` | 审查动作的菜单名，修改后需重新执行 `stai install` |
| `sourcetree.review_shortcut_key_code` | `15` | 审查快捷键键码，`15` 为 R |
| `sourcetree.review_shortcut_modifiers` | `524288` | 修饰键，`524288` 为 Option (⌥) |
| `sourcetree.review_shortcut_display` | `⌥R` | 审查快捷键显示文本 |
| `log.path` | `~/Library/Logs/stai.log` | 诊断日志路径，记录每次 `gen` / `review` / 钩子的运行情况和剪贴板结果 |
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
cmd/stai/        CLI 入口与子命令（gen / hook / review / mergetool / install / uninstall）
internal/ai      OpenAI 兼容接口客户端、提交信息生成与提交前审查
internal/config 分层配置加载
internal/git     git 命令封装（暂存区 diff、钩子路径）
docs/            设计说明（design.md）
```

## 路线图

- **M1 — commit 信息生成**（已完成）：`stai gen`（生成并复制）、`stai hook prepare-commit-msg`（空信息兜底）、`stai install`。
  验收标准：在 SourceTree 中全流程跑通，不离开 GUI。
- **M2 — 提交前 review**（已完成）：`stai review`（自定义操作 ⌥R，建议模式）+
  `stai hook pre-commit`（严格模式开关 `review.strict`）+ `stai uninstall`。
  验收标准：SourceTree 中 ⌥R 出报告；strict 模式下高危问题阻断提交，AI 故障不阻断。
- **M3 — 冲突合并协助**：`stai mergetool` 接入 `git config mergetool`，
  逐冲突块提供「解释 + 推荐 + 采纳/自行修改」的交互界面。
