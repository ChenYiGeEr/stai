# AC-5 修正：SourceTree 拉起时 Terminal.app 不再一闪就关

叠在已通过 QA 的 `c85a59a` 之上。AC-5 的退出码回传和「打不开终端就不写 `$MERGED`」没有改。改的是 macOS 上**怎么打开** Terminal.app。

## 现场现象

SourceTree 菜单「动作 → 解决冲突 → 启动外部合并工具」启动 `stai mergetool` 时，stdin 不是 TTY。`c85a59a` 用 `osascript` 执行 `tell application "Terminal" … do script`。这是发给 Terminal 的 Apple Event。

所有者的二进制是本机链接出来的，标识是 `a.out`，没有 Team ID，也没有 Info.plist。SourceTree 是 Atlassian 的 Developer ID 签名，但真正发 Apple Event 的是随后启动的 `stai`。TCC 对这个进程静默拒绝（Apple event -1743）：不弹出自动化权限框，系统设置的「自动化」里也没有 SourceTree 或 stai。Terminal 窗口闪一下就关，`$MERGED` 不会被写入，冲突保持未合并。

Grok Bot 自己的 shell 若事先获准控制 Terminal，可以看到冲突界面。那条路径不是 AC-5 的证明，这次也不把它当成通过。

## 改动

不再向 Terminal 发 `tell application` / `osascript`。

无 TTY 时，macOS 把同一条交互命令写成一个可执行的 `stai-mergetool.command`（`#!/bin/sh`，里面带 `STAI_MERGETOOL_TERMINAL=1`，并用绝对路径调用当前的 `stai`），然后执行：

```text
/usr/bin/open -a Terminal <该 .command 文件>
```

`/usr/bin/open` 走 Launch Services 的「用 Terminal 打开这个文档」。`.command` 文件会在新窗口里前台跑完整个按键会话，窗口在 `stai mergetool` 还在等按键时不会退出。会话结束才把退出码写到状态文件，父进程读到 0 或 1 再返回给 `git mergetool`。父进程自己不写 `$MERGED`。

这条路径不要求 stai 或 SourceTree 拥有「控制 Terminal」的自动化权限，因此也不依赖 Grok Bot 已经点过的那次授权。没有伪造签名，也没有把 adhoc 签名说成正式签名；TCC 自动化名单里可以没有 stai，因为这里不再发那条会被 -1743 拒绝的事件。

非 macOS 仍然只打印原因、退出 1、不写 `$MERGED`。

## 仍被锁住的行为

```bash
go test ./cmd/stai ./internal/...
```

`mergetool-no-tty-terminal`：

- 会话里全部解决 → 退出码 0，`$MERGED` 与 AC-2 期望一致
- 会话里跳过或退出 → 退出码 1，冲突标记还在
- 打不开终端 → 退出码 1，`$MERGED` 的字节和 mtime 不变

`./internal/git` 的 `TestBranchDiffDir` 仍与本组无关。

## 在 Mac 上重测

1. 用这一提交重新编译 `stai`，确认 `git config --get mergetool.stai.cmd` 指向这份新二进制。
2. 在一个文本冲突上，用 SourceTree 自己点「动作 → 解决冲突 → 启动外部合并工具」。不要从 Grok Bot 的 shell 里先授权再测。
3. Terminal.app 应留下一个能操作的窗口，里面是冲突块的解释、推荐和按键提示，而不是闪一下就关。
4. 全部按 `a` / `o` / `t` 解决后，git 把这次 mergetool 视为成功（退出 0）。中途 `s` 或 `q` 则冲突标记还在，git 视为失败（退出 1）。
5. 系统设置 → 隐私与安全性 → 自动化里不需要预先有 stai。若系统仍然弹出权限框，它必须看得见，不能再是无提示的 -1743。

## 截图

SourceTree 加 Terminal.app 的实机截图仍然没有。这台云主机是 Linux，没有 SourceTree，也没有 Terminal.app。不能把伪造画面或本机终端模拟当成那张证明。AC-5 的这一项仍未通过，要在上面的 Mac 重测里补。
