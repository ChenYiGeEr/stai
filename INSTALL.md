# 安装 stai

一次安装，让 SourceTree 拥有 AI 辅助。

## One-liner

**macOS**

```bash
curl -fsSL https://raw.githubusercontent.com/chenyiGeEr/stai/v0.1.0/install.sh | bash
```

> 直接管道执行脚本意味着你没有预先查看。如果你想先审查，可以下载后执行：
>
> ```bash
> curl -fsSL https://raw.githubusercontent.com/chenyiGeEr/stai/v0.1.0/install.sh -o install.sh
> # 审查 install.sh
> bash install.sh
> ```
>
> 脚本和 `go install` 目标都钉到 `v0.1.0`，不会跟随 `main` 分支漂移。

它会做什么：

- 检查是否已安装 Go 1.23+（stai 需要 Go 工具链）；
- 自动找一个可写且在 PATH 中的目录（优先 `~/.local/bin`，其次 `~/bin`，最后 `/usr/local/bin`）；
- 通过 `go install github.com/chenyiGeEr/stai/cmd/stai@v0.1.0` 安装 `stai` 二进制；
- **不修改当前仓库**，也**不写入 SourceTree 配置**。

安装完成后，在任意 git 仓库根目录执行 `stai install` 来写入钩子和 SourceTree 自定义操作。

想先预览安装行为？使用 `--dry-run`：

```bash
curl -fsSL https://raw.githubusercontent.com/chenyiGeEr/stai/v0.1.0/install.sh | bash -s -- --dry-run
```

## 安装方式一览

| 方式 | 命令 | 适用场景 |
|---|---|---|
| **One-liner（推荐）** | `curl -fsSL .../install.sh \| bash` | 快速安装，需要 Go |
| **`go install`** | `go install github.com/chenyiGeEr/stai/cmd/stai@latest` | 习惯 Go 工具链 |
| **源码构建** | `git clone ... && cd stai && go build -o stai ./cmd/stai` | 需要修改或调试 |

> 如果你使用 `go install` 或源码构建，请把生成的 `stai` 二进制放到 PATH 中的目录（例如 `~/.local/bin`）。

## 前置条件

- **macOS**（SourceTree 集成依赖 macOS 的 `osascript`、`pbcopy` 与 SourceTree 的 `actions.plist`）；
- **SourceTree**（已在 4.2.19 上验证）；
- **Go 1.23+**（one-liner、`go install` 和源码构建都需要）；
- 一个 **OpenAI 兼容的模型服务**，默认为本机 Ollama（`http://localhost:11434/v1`），也可指向 LM Studio 或 API 网关。

## 源码构建

```bash
git clone https://github.com/chenyiGeEr/stai.git
cd stai
go build -o stai ./cmd/stai

# 可选：复制到 PATH
mkdir -p ~/.local/bin
cp stai ~/.local/bin/
```

## 配置与使用

在仓库根目录执行：

```bash
stai install                  # 写入三个钩子 + 注册九个 SourceTree 自定义操作
stai install -no-sourcetree   # 只装钩子，不动 SourceTree
stai uninstall                # 移除 install 写入的一切（只删 stai 自己写的）
```

首次 `stai install` 且 `~/.config/stai/config.toml` 不存在时，会启动交互式向导，让你选择语言、填写模型服务地址/密钥、选择模型，以及挑选要注册的 SourceTree 自定义操作。

详细用法见 [README.md](README.md)。

## 验证安装

```bash
stai version
stai doctor
```

`stai version` 应输出 `v0.1.0`。

`stai doctor` 会检查：

- 全局配置文件是否存在；
- 模型服务是否可达；
- 当前目录是否是 git 仓库；
- `prepare-commit-msg` / `pre-commit` / `pre-push` 钩子是否已安装；
- SourceTree 自定义操作是否已注册；
- 日志文件是否可写。

如果 `stai version` 提示「command not found」，请确认安装目录（例如 `~/.local/bin`）已经加入 PATH。
