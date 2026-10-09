# stai — SourceTree 的 AI 伴侣工具

stai 是一个外挂式的 Git 工作流 AI 助手：它不是 SourceTree 的插件（见下文「为什么不是插件」），
而是一个独立的命令行工具，通过 Git 标准机制和 SourceTree 的自定义操作挂进你的工作流，
提供三类能力：

1. **commit 信息生成** —— 读暂存区 diff，按 Conventional Commits 生成提交信息；
2. **提交前 code review** —— 对暂存区做 AI 审查，输出问题清单；
3. **冲突合并协助** —— 接管 mergetool，逐冲突块给出解释与推荐解法。

## 为什么不是插件

关键事实（已在安装于本机的 Sourcetree 4.2.19 上验证）：

- SourceTree **没有任何插件机制**——自带 framework 只有 Sparkle（自动更新）和
  CocoaLumberjack（日志），二进制中没有插件加载点；
- 它的可扩展面是 Git 本身外加三个官方口子：
  1. **Git 钩子**（SourceTree 走系统 git，`prepare-commit-msg` / `pre-commit` 等均生效）；
  2. **「动作 → 自定义操作」**（菜单注册外部命令，自动传入仓库路径、选中文件）；
  3. **mergetool / difftool 包装**（可配置外部合并工具，附 Araxis 等包装脚本先例）；
  4. `sourcetree://` URL scheme（备用通道）。

因此 stai 的定位是 **Git 侧伴侣**：所有功能都建立在 git 标准机制上，
SourceTree 升级几乎不会影响我们；换用其他 Git GUI（Tower、Fork、命令行）同样可用。

## 设计决策（已共识）

| 决策点 | 结论 |
| --- | --- |
| 接入形态 | 外部伴侣工具：git 钩子 + SourceTree 自定义操作 + mergetool 包装 |
| 交付顺序 | M1 commit 信息生成 → M2 提交前 review → M3 冲突合并协助 |
| AI provider | OpenAI 兼容协议可配置，默认指向本机（Ollama / LM Studio），可改网关 |
| 使用范围 | 先自用：CLI + 一键导入 SourceTree 自定义操作 |
| 语言 | Go（单静态二进制，钩子冷启动毫秒级，团队分发只需拷贝一个文件） |
| commit 信息交互 | 双通道：自定义操作「生成并填入提交框」（框内审阅，失败回退复制）+ 空信息时钩子兜底 |
| 冲突合并中 AI 角色 | 解说员 + 逐冲突建议，AI 不直接写文件（自动解决留作进阶开关） |
| review 阻断策略 | 先建议后阻断：默认不拦截，严格模式（pre-commit 返回非零）用配置开关，默认关 |
| 配置作用域 | 双层：全局 `~/.config/stai/config.toml` + 仓库级 `.stai.toml`（可提交，团队共享约定） |

## 使用方式

### 安装与注册

在仓库根目录执行（全局安装后任意仓库都可运行）：

```bash
stai install                  # 写 prepare-commit-msg 钩子 + 注册 SourceTree 自定义操作
stai install -no-sourcetree   # 只装钩子，不动 SourceTree
```

`install` 做了两件事：

1. 向当前仓库写入 `prepare-commit-msg` 钩子（内容只有一行调用，指向 stai 二进制）；
2. 向 SourceTree 的自定义操作存储写入条目：
   `~/Library/Application Support/SourceTree/actions.plist`（NSKeyedArchiver
   格式，schema 取自 4.2.19 实机生成的条目），菜单项 **AI 生成提交信息**，
   参数 `gen $REPO`（SourceTree 会把 `$REPO` 展开为仓库路径传入），
   快捷键 **⌥G**（安装后可在 SourceTree 设置 → 自定义操作中自行修改）。

该操作是幂等的：重复执行会替换指向本二进制或同名的旧条目，其他工具
创建的条目原样保留。
注意：**重装前先退出 SourceTree**——它在运行时会用内存中的动作列表
覆盖 `actions.plist`，导致写入丢失。注册后需重启 SourceTree 菜单才会出现。

### 日常流程（M1：commit 信息生成）

两条通道互补，互不干扰：

1. **自定义操作（框内审阅，推荐）**
   SourceTree 里暂存改动 → 菜单栏「动作 → 自定义操作 → AI 生成提交信息」→
   模型直接生成并填入 SourceTree 的提交信息输入框（辅助功能脚本点击聚焦后
   经剪贴板粘贴写入，SourceTree 的数据模型随之更新，点击输入框不会丢失；
   粘贴后恢复原有剪贴板内容，光标停在文本末尾）→ 在提交框中审阅、修改后提交。
   填充失败（未授权、窗口未找到等）自动回退为复制到剪贴板。
   等价命令行：`stai gen`；`stai gen --edit` 则先弹出可编辑对话框再填入提交框。

   首次自动填充前，需在 系统设置 → 隐私与安全性 → 辅助功能 中勾选 stai
   二进制（系统会自动弹授权提示）；二进制路径变更后可能需要重新勾选。

2. **钩子兜底（零操作）**
   直接提交且提交框留空时，`prepare-commit-msg` 钩子会自动生成信息填进
   提交框。merge / squash / commit（--amend）类提交和已有信息的提交一律
   不碰；生成失败只打印警告，绝不阻断提交（内部 120 秒超时保护）。

临时禁用钩子（不卸载）：`STAI_DISABLE=1 git commit ...`

### 环境变量覆盖

| 变量 | 作用 |
| --- | --- |
| `STAI_BASE_URL` / `STAI_API_KEY` / `STAI_MODEL` | 覆盖 provider 配置 |
| `STAI_DISABLE` | 非空时钩子直接放行 |

## 配置

全局 `~/.config/stai/config.toml`：

```toml
[provider]
base_url = "http://localhost:11434/v1"   # 默认本机 Ollama
api_key  = ""                             # 本地模型留空
model    = "qwen2.5-coder:7b"

[commit]
style    = "conventional"                 # conventional / free
language = "zh-CN"

[review]
strict = false                            # true 时 pre-commit 发现高危问题才阻断
```

仓库级 `.stai.toml`（提交进库，团队共享）：同结构，字段覆盖全局。

## 路线图

- **M1 — commit 信息生成**：`stai gen`（生成+复制）+ `stai hook prepare-commit-msg`
  （空信息兜底）+ `stai install`。验收：SourceTree 里全流程跑通，不离开 GUI。
- **M2 — 提交前 review**：`stai review`（自定义操作，建议模式）+ pre-commit 严格模式开关。
- **M3 — 冲突合并协助**：`stai mergetool` 包装进 `git config mergetool`，
  逐冲突块「解释 + 推荐 + 采纳/自改」TUI。

## 目录结构

```
cmd/stai/        CLI 入口与子命令（gen / hook / review / mergetool / install）
internal/        实现包（按里程碑逐层填充）
docs/            设计文档与决策记录
```

## 开发

```bash
go build ./...        # 构建
go run ./cmd/stai help
```

> 注意：本机 Homebrew 的 `go` shim 指向已删除的旧版本目录，临时可用
> `GOROOT=/opt/homebrew/opt/go/libexec /opt/homebrew/opt/go/bin/go` 代替，建议跑一次
> `brew relink go` 修复。
