# 第 2 组 Proof：注册与卸载 mergetool.stai

切片：`stai 绑定切片 2026-11-12：文本冲突 mergetool`。第 1 组（AC-2、AC-3、AC-4、AC-6）已在 `418c91119153921fa8db6d21d6ca0ddbff8cc501` 通过官方 QA。本组只加 AC-1 和 AC-8，提交叠在该 SHA 之上，不改写它。

`stai install` 在当前仓库写入：

- `mergetool.stai.cmd` = `'<stai 可执行文件>' mergetool --base "$BASE" --local "$LOCAL" --remote "$REMOTE" --merged "$MERGED"`
- `mergetool.stai.trustExitCode=true`
- `merge.tool` 未设置或已经是 `stai` 时，设为 `stai`
- `merge.tool` 被别的工具占用时先问。只有明确回答 `y` / `yes` 才改成 `stai`。拒绝或非交互都不改 `merge.tool`，但仍然注册 `mergetool.stai`，因此 `git mergetool --tool=stai` 可用

`stai uninstall` 删除 `mergetool.stai.cmd` 和 `mergetool.stai.trustExitCode`。若这次或以前的 stai 安装把 `merge.tool` 设成了 `stai`，就恢复当时记下的旧值；原来没有则取消 `merge.tool`。当前值若是别的工具，不动它。

测试把 `HOME`、`GIT_CONFIG_GLOBAL`、`GIT_CONFIG_SYSTEM` 指到临时目录，只在临时仓库里写本地 git config，不调用会改 SourceTree `actions.plist` 的安装路径。

## 怎么跑

```bash
go test ./cmd/stai ./internal/...
```

`./cmd/stai`、`./internal/mergetool`、`./internal/i18n`、`./internal/config`、`./internal/ai`、`./internal/color` 通过。`./internal/git` 的 `TestBranchDiffDir` 仍失败：本机 `git init` 默认分支是 `master`，该测试假定有 `main`。`internal/git` 不在这次改动里。

下面的期望文件就是测试逐键核对的锁。`!键名` 表示该键必须不存在。`cmd` 必须等于 shell 引用后的可执行文件路径，再接 `cmd.suffix`。

## AC-1 `install-mergetool-free`

安装前：仓库里没有 `merge.tool`，也没有 `mergetool.stai`。非交互安装。

安装后（`cmd/stai/testdata/install-mergetool-free/expected.txt`）：

```
merge.tool=stai
mergetool.stai.trustExitCode=true
mergetool.stai.staiOwnsMergeTool=true
mergetool.stai.staiMergeToolScope=unset
!mergetool.stai.staiPreviousMergeTool
```

`mergetool.stai.cmd` 以这段结尾（`cmd.suffix`）：

```
mergetool --base "$BASE" --local "$LOCAL" --remote "$REMOTE" --merged "$MERGED"
```

`$BASE`、`$LOCAL`、`$REMOTE`、`$MERGED` 按字面量存进 git config，安装时不被 shell 展开。

## AC-1 `install-mergetool-occupied`

安装前（`before.txt`）：

```
merge.tool=vimdiff
```

| 场景 | 询问 | 安装后 |
| --- | --- | --- |
| 同意 | 提示一次，正文含 `vimdiff` 和「是否改为 stai」 | `merge.tool=stai`，`staiPreviousMergeTool=vimdiff`，`staiMergeToolScope=local`，`trustExitCode=true`，`cmd` 同上 |
| 拒绝 | 同样询问，回答否 | `merge.tool` 仍是 `vimdiff`；`cmd` 与 `trustExitCode=true` 已写入；没有所有权记录 |
| 非交互 | 不询问 | 与拒绝相同：`merge.tool` 仍是 `vimdiff`，`mergetool.stai` 已注册 |

占用且全局（不是本地）已有 `merge.tool=vimdiff` 时，同意会在本仓库写入 `merge.tool=stai` 并记下 `staiMergeToolScope=foreign`。卸载时只删掉本地这一层，有效值回到全局的 `vimdiff`，全局文件里不会出现 `mergetool.stai`。

### 询问界面

真实终端里跑 `stai install -no-sourcetree`（stdin 是 TTY，`merge.tool=vimdiff`）时停在确认提示。截图：

`docs/proofs/mergetool-occupied-ask.png`

画面上的提示是：

```
已注册 mergetool.stai
merge.tool 当前是 vimdiff。是否改为 stai? 拒绝仍会注册 mergetool.stai,之后可用 git mergetool --tool=stai [y/N]:
```

空回车按否处理，只有 `y` / `yes` 才改 `merge.tool`。

## AC-8 `uninstall-mergetool-restore`

| 场景 | 卸载前 | 卸载后 |
| --- | --- | --- |
| 当初没有 `merge.tool` | 本次安装把它设成 `stai` | `merge.tool`、`mergetool.stai.cmd`、`trustExitCode` 和所有权记录都不存在 |
| 当初是 `vimdiff` 且用户同意 | 记下旧值后再设成 `stai`；再次安装时 `merge.tool` 已是 `stai`，不再询问，旧值仍是 `vimdiff` | `merge.tool=vimdiff`，`cmd` 与 `trustExitCode` 已删除 |
| 安装之后用户把 `merge.tool` 改成 `opendiff` | `opendiff` | `merge.tool` 仍是 `opendiff`，`cmd` 与 `trustExitCode` 已删除 |

## 这一组没有做

- AC-5：stdin 不是 TTY 时不打开 Terminal.app
- AC-7：没有改 `docs/design.md` 或 `SECURITY.md`，也没有把切片放到 `design.md` 顶部
- 没有改 PR https://github.com/ChenYiGeEr/stai/pull/1 或分支 `cursor/sourcetree-interaction-group1-edd3`
- 没有改 SourceTree `actions.plist` 的安装、卸载和 doctor，也没有改第 1 组的冲突解决行为
- `auto_apply` 仍然只被忽略
