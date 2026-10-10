# stai 设计说明

本文记录 stai 的定位、接入方式与关键决策。使用方法见仓库根目录的 [README.md](../README.md)。

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

## 设计决策

| 决策点 | 结论 |
| --- | --- |
| 接入形态 | 外部伴侣工具：git 钩子 + SourceTree 自定义操作 + mergetool 包装 |
| 交付顺序 | M1 commit 信息生成 → M2 提交前 review → M3-A/B/C 分支级辅助（PR 描述、分支 review、stash 信息）→ M3 冲突合并协助（暂缓） |
| AI provider | OpenAI 兼容协议可配置，默认指向本机（Ollama / LM Studio），可改网关 |
| 使用范围 | 先自用：CLI + 一键导入 SourceTree 自定义操作 |
| 语言 | Go（单静态二进制，钩子冷启动毫秒级，团队分发只需拷贝一个文件） |
| commit 信息交互 | 双通道：自定义操作「生成并复制」（系统通知展示全文，Cmd+V 粘贴）+ 空信息时钩子兜底 |
| PR 描述/stash 信息交互 | 复用 commit 信息的「生成并复制」通道，无新交互形式 |
| 分支 review 阻断策略 | 复用 M2：默认建议，pre-push 严格模式用独立 `pre_push.strict`，默认关 |
| 冲突合并中 AI 角色 | 解说员 + 逐冲突建议，AI 不直接写文件（自动解决留作进阶开关） |
| review 阻断策略 | 先建议后阻断：默认不拦截，严格模式（pre-commit 返回非零）用配置开关，默认关 |
| 配置作用域 | 双层：全局 `~/.config/stai/config.toml` + 仓库级 `.stai.toml`（可提交，团队共享约定） |
