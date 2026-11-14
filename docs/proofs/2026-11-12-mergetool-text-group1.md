# 第 1 组 Proof：文本冲突 mergetool

切片：`stai 绑定切片 2026-11-12：文本冲突 mergetool`。所有者于 2026-11-14 10:02（Asia/Shanghai）批准，范围未改。本交付只覆盖解锁顺序第 1 组：AC-2、AC-3、AC-4、AC-6。

命令：`stai mergetool --base --local --remote --merged`。一次调用处理一个文件。按键为 `a` `o` `t` `p` `e` `s` `q`。`a` 只在当前块有可用推荐时生效。`o` 写入该块的 ours（冲突标记中的 `--local` 一侧），`t` 写入 theirs（`--remote` 一侧）。`p` 只在当前会话打印预览。`e` 使用 `GIT_EDITOR`，否则 `VISUAL`，否则 `EDITOR`；都没有时只说明如何设置。`s` 保留当前块的冲突标记。`q`、输入结束或中断保留未处理块的冲突标记并退出 1。配置里的 `auto_apply` 被忽略，不改变任何行为。

## 怎么跑

```bash
go test ./internal/mergetool/ ./internal/ai/ ./internal/i18n/ ./internal/config/ ./cmd/stai/
```

本环境用 Go 1.23.12 跑过上述命令，结果为通过。fixture 测试在 `internal/mergetool`，断言实际 `$MERGED` 与期望文件字节级相同，并核对退出码。下面的 SHA-256 是期望文件的摘要；测试失败就会在字节不一致时停下，所以通过即表示实际 `$MERGED` 与这些字节相同。

`go test ./internal/git` 里的 `TestBranchDiffDir` 在本环境失败：`git init` 的默认分支是 `master`，该测试假定存在 `main`。`internal/git` 不在这次改动里。

## AC-2 `mergetool-text-all-resolved`

三路输入：`base.txt`、`local.txt`、`remote.txt`，冲突文件 `merged.txt`（三个文本冲突块）。按键 `aot`（`keys.txt`）。退出码 0（`exit.txt`）。

- `a`：写入推荐 `MERGED-1`，不是 ours/theirs 原文。
- `o`：写入 `OURS-2`。模型给该块的推荐是 `SHOULD-NOT-APPLY-2`，文件里没有这串文本。
- `t`：写入 `THEIRS-3`。模型推荐 `SHOULD-NOT-APPLY-3` 没有写入。

实际 `$MERGED` 与 `expected.txt` 一致，无冲突标记。SHA-256 `5d6f30b4e3e03355215fe213c52aaea11cc6e23d14385d34b07de0ec7951e533`（97 字节）：

```
alpha
MERGED-1
gap-a
gap-b
gap-c
gap-d
gap-e
OURS-2
gap-f
gap-g
gap-h
gap-i
gap-j
THEIRS-3
omega
```

## AC-3 `mergetool-text-skip`、`mergetool-text-quit`

两个文本冲突块。退出码都是 1。未处理块仍是原来的冲突标记，推荐没有写进去。

| fixture | 按键 | 已接受 | 禁止出现在 `$MERGED` 的推荐 | 期望 SHA-256 |
| --- | --- | --- | --- | --- |
| `mergetool-text-skip` | `as` | `ACCEPTED-1` | `FORBIDDEN-REC-2` | `dec58b03ddeac8a1b38aab62aceb170069d30f15e6f8026d1a521b4e442a6635` |
| `mergetool-text-quit` | `aq` | `ACCEPTED-Q1` | `FORBIDDEN-REC-Q2` | `d425e9774dd7fd6132a38cdd8941b5342801414c7cf74c8cbdf8d4a9fbd8cb68` |

跳过用例的实际 `$MERGED`：

```
alpha
ACCEPTED-1
gap-a
gap-b
gap-c
gap-d
gap-e
<<<<<<< HEAD
OURS-2
=======
THEIRS-2
>>>>>>> topic
omega
```

中途 `q` 的实际 `$MERGED` 同样保留第二块的冲突标记，只是第一块为 `ACCEPTED-Q1`。

## AC-4 `mergetool-ai-failure`

顾问每次都返回错误 `provider unreachable`，并带上一段本应被丢掉的模型文本 `MODEL-GUESS-DO-NOT-WRITE`（`guess.txt`，SHA-256 `8356c10cf5cc84985e9abc4d762a6c9bbac503cffaba56877ecc912447c28ca8`）。界面打印失败，并给出不含 `a` 的按键提示（`o` `t` `e` `s` `q`）。`$MERGED` 和界面都不包含这段猜测。

| 场景 | 按键 | 退出码 | 实际 `$MERGED` | SHA-256 |
| --- | --- | --- | --- | --- |
| 全部手解 | `apot`（`a` 被拒绝，`p` 只预览两侧，`o` 再 `t`） | 0 | `OURS-1` 与 `THEIRS-2`，无冲突标记 | `d7f4372c140c4239da10088e38c15054e2031878ed3ff7ac3f4880f594d3aa43` |
| 未解完 | `q` | 1 | 与输入 `merged.txt` 相同，冲突标记还在，mtime 未变 | `426305278f2b725ee6a66779c869eb3efeeea940c815ab017d7051dabb5ee7ad` |
| 外部编辑器 | `e` | 0 | `FROM-EDITOR`，编辑器打开时文件仍带冲突标记 | `4ad49ee29fab3ae28cc247ef227845f8f3099a4546be4a1f951f9766a4535da0` |

`STAI_LANG` / `[global] lang` 切到 `en` 时，失败句和按键提示变为英文。未设置 `GIT_EDITOR`、`VISUAL`、`EDITOR` 时只提示如何设置，不改文件。

## AC-6 `mergetool-unsupported-conflict`

退出码 1（`exit.txt`）。这些用例不读取按键，也不调用模型。`$MERGED` 字节和 mtime 都不变；缺失的路径不会被写成一份猜测结果。

| 子用例 | 判定 | 实际 `$MERGED` |
| --- | --- | --- |
| `binary/` | 文件含 NUL（`mer\0ged\n`） | 仍为 `6d 65 72 00 67 65 64 0a`，SHA-256 `7031fba01320c378e911e8d789891fe7d73e4899123bf6959b4645ccc2a641c0` |
| `delete/` | `--local` 文件不存在 | 仍为 `this-is-not-a-resolution\n`，SHA-256 `d3bea96fdc9af53e9c5faf5a443c0b134d1ffb09caac522ad4959f5bea38dc8b` |
| `submodule/` | 正文是 `Subproject commit <sha>` | 冲突标记原样保留，SHA-256 `181ee476180cfd26afef51026b7b8978d66bb1872ba27a55082dbd04812c0ca0` |
| `rename/body.txt` | 真实 rename/rename：`old.txt` 仅 stage 1，`ours.txt` / `theirs.txt` 为单侧添加 | `ours.txt` 与 `theirs.txt` 仍为 `body\n`；`old.txt` 不会被创建 |
| gitlink | 索引 mode `160000` 的三条 stage | 不创建 `lib` |

界面文案是「本切片不处理二进制/删除/重命名/子模块冲突，未写入解决结果。」

## 这一组故意没做

- AC-1、AC-8：不改 `stai install` / `stai uninstall`，不写 `mergetool.stai` 或 `merge.tool`。
- AC-5：stdin 不是 TTY 时不打开 Terminal.app。测试用 fixture 喂按键，没有按键序列就不会写 `$MERGED`。
- AC-7：不改 `docs/design.md`，不改 `SECURITY.md`，不把切片放到 `design.md` 顶部。
- `auto_apply`：配置里出现该键时加载成功并忽略，没有自动写入。
- 不改 SourceTree `actions.plist` 的安装与 doctor、进程名判断、交互上下文矩阵，也不改 2026-11-13 / 2026-11-14 的交互切片文档或 fixture。
- 不改 PR https://github.com/ChenYiGeEr/stai/pull/1，不改分支 `cursor/sourcetree-interaction-group1-edd3`，也不改提交 `7d6c140`、`47495be`、`b97320e`。
- 不做 apply-fixes、审查补丁 JSON、`split --apply`、MCP 写入工具、钩子里的 mergetool，也不做非文本冲突的解决流程。
