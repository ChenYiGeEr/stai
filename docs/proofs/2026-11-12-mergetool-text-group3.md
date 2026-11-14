# 第 3 组 Proof：无 TTY 时把退出码从终端会话传回

切片：`stai 绑定切片 2026-11-12：文本冲突 mergetool`。第 1 组 `418c911` 和第 2 组 `0607e2e` 已通过官方 QA。本组只加 AC-5，提交叠在 `0607e2e` 之上。

stdin 是 TTY 时，`stai mergetool` 仍在当前进程里做第 1 组的交互。stdin 不是 TTY 时，当前进程不读按键，也不写 `$MERGED`。macOS 上它让 Terminal.app 再执行同一条命令（带 `STAI_MERGETOOL_TERMINAL=1`），等那个会话把退出码写到状态文件，然后把 0 或 1 交回给 `git mergetool`。其他系统打不开 Terminal.app：打印原因，退出 1，不写 `$MERGED`。

## 怎么跑

```bash
go test ./cmd/stai ./internal/...
```

`./cmd/stai`、`./internal/mergetool`、`./internal/i18n`、`./internal/config`、`./internal/ai`、`./internal/color` 通过。`./internal/git` 的 `TestBranchDiffDir` 仍因 `git init` 默认分支是 `master` 而失败，与本组无关。

## fixture `mergetool-no-tty-terminal`

目录：`cmd/stai/testdata/mergetool-no-tty-terminal/`。调用方的 stdin 是管道，不是 TTY。测试把「终端会话」注入成第 1 组同一套 `mergetool.Run`：按键和期望文件与 AC-2 / AC-3 相同。父进程只转发退出码，自己的 stdout / stderr 为空。

| 子目录 | 会话里的按键 | 父进程退出码 | `$MERGED` |
| --- | --- | --- | --- |
| `resolve/` | `aot`（与 `mergetool-text-all-resolved` 相同） | 0 | 与该 fixture 的 `expected.txt` 逐字节相同，无冲突标记 |
| `quit/` | `aq`（与 `mergetool-text-quit` 相同） | 1 | 与该 fixture 的 `expected.txt` 逐字节相同；未处理块仍有冲突标记，没有 `FORBIDDEN-REC-Q2` |
| `no-terminal/` | 管道里放了 `aot`，但本机不是 macOS，终端打不开 | 1 | 字节和 mtime 都不变。stderr 为「无法打开终端: 本机不是 macOS，无法打开 Terminal.app。未写入合并结果。」 |

另外两条锁：

- 注入的打开函数返回错误时，退出码是 1，`$MERGED` 的字节和 mtime 不变，原因出现在 stderr。
- 子进程已经带 `STAI_MERGETOOL_TERMINAL=1` 但 stdin 仍不是 TTY 时，不再打开第二个终端，也不写 `$MERGED`，退出 1。

父进程在打开终端失败或还没有会话时，不会调用解决逻辑，因此不会在没有按键序列的情况下写入 `$MERGED`。

## SourceTree + Terminal.app 截图

这一项没有截到，不能算通过。

这台云主机是 Linux，没有 macOS，也没有 SourceTree，打不开 Terminal.app。按切片要求，不能把伪造的 SourceTree 合并画面或本机终端模拟画面当成那张证明。AC-5 要求的「SourceTree 发起合并时出现带冲突块 UI 的 Terminal」截图在这里是空缺，需要在装有 SourceTree 和 Terminal.app 的 Mac 上补。
