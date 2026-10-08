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
| commit 信息交互 | 双通道：自定义操作「生成并复制」（人确认）+ 空信息时钩子兜底 |
| 冲突合并中 AI 角色 | 解说员 + 逐冲突建议，AI 不直接写文件（自动解决留作进阶开关） |
| review 阻断策略 | 先建议后阻断：默认不拦截，严格模式（pre-commit 返回非零）用配置开关，默认关 |
| 配置作用域 | 双层：全局 `~/.config/stai/config.toml` + 仓库级 `.stai.toml`（可提交，团队共享约定） |

## 接入方式（M1 目标形态）

```bash
# 安装后注册：写 git 钩子 + 注册 SourceTree 自定义操作
stai install

# SourceTree 菜单里会出现：
#   动作 → AI 生成提交信息   # 生成并复制到剪贴板
#   动作 → AI 审查暂存区     # M2
# 每次提交时空信息由 prepare-commit-msg 钩子兜底生成
```

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

