# stai 绑定切片 2026-11-14：SourceTree 核对版本改为 4.2.20

Decision needed: approve this cut | reject a listed out item.

- 产品：stai（CLI + SourceTree 4.2.20 macOS 自定义操作与 Git 钩子）
- 日期：2026-11-14（亚洲/上海）
- 状态：已批准。日期：2026-11-14（亚洲/上海）。
- 与既有切片的关系：本切片是对已批准的「stai 绑定切片 2026-11-13：SourceTree 现有交互（摆放、反馈、说法）」的中途范围变更。原因：机主本机 SourceTree 已更新到 4.2.20（build 324，路径 `/Applications/Sourcetree.app`，机主已确认），而 2026-11-13 切片把核对版本锁在 4.2.19。本切片不替代 2026-11-13 全文；2026-11-13 的范围内/范围外与 AC 仍有效，仅下列条款以本切片为准：（1）核对与验收版本改为 4.2.20（macOS）；（2）AC-4 类型对照样本改在 4.2.20 界面手动勾选并保存后采集；（3）文档中将行为或支持范围锁在 4.2.19 的表述改为 4.2.20。禁止用聊天修改已批准的 2026-11-13 正文，或用聊天改本切片范围。本切片不替代、不修改、不推进「stai 绑定切片 2026-11-12：文本冲突 mergetool」。
- 活文档：仓库内 `docs/design.md`（本切片与 2026-11-13 一并作为 PM 结论优先）；批准后将本切片置于该文档顶部（或所有者指定的切片路径），并保留 2026-11-13 切片，勿改写其正文。
- 既有进度（本切片的前提，不在本切片里重做第 1 组行为验收）：第 1 组 AC-1/2/3 已由 QA 在 SHA `7d6c140ab0cc7a41b197074f8768eacca4623e30`、[PR #1](https://github.com/ChenYiGeEr/stai/pull/1) 上全部 PASS；当时文档与截图按 4.2.19 表述。第 2 组（AC-4/AC-5）此前已批准开工，但停在 AC-4 解锁门槛。

## 成功标准

Decision needed: approve this cut.

在机主本机 SourceTree 4.2.20（macOS，build 324）上完成 2026-11-13 第 2、3 组验收：上下文矩阵与类型写入与 4.2.20 界面保存样本字节级一致；长文可在已有 Show Full Output 窗口读完；`install` / `doctor` 按原锁检查。产品文档将本工作的核对版本写为 4.2.20（macOS），不再写成仅限或锁定 4.2.19。第 1 组已 PASS 的行为结论保留。不自动改工作区或提交框，AI 故障不阻断提交或推送。

## 范围内

Decision needed: approve this in-list.

- 核对版本改为 SourceTree 4.2.20（macOS）。文档、注释、i18n 以及本工作相关说明中，凡把 SourceTree 行为或支持/核对范围写成「仅限 4.2.19」「限定为 4.2.19」或同等表述的，改为 4.2.20（macOS）。仍不得推广到未验证的 Windows 或未验证的其他版本。2026-11-13 写明的主路径与警告保留：⌥G → 通知 → 提交框 Cmd+V；打开提交框不跑 `prepare-commit-msg`；空框点提交先出「Empty Commit Message / 你没有填写提交说明」；CLI 钩子兜底分节另述。上述界面行为在无新证据表明 4.2.20 不同的前提下仍作为本切片的指定行为；若验收时 4.2.20 的实际文案或时序不同，停止并报告，不得软通过，也不得用聊天改切片。
- 第 1 组处理：AC-2、AC-3 的 PASS 结论保留，不因版本号变更而重跑或补审。AC-1 中除版本表述外的文案结论（不再出现「填入提交框」或 “fills in the commit box” 同类表述，主路径与警告、CLI 分节）保留，不重跑第 1 组行为验收。PR #1 / SHA `7d6c140` 上按 4.2.19 表述的文档与截图作为第 1 组历史证明保留，不必回改那次提交。第 2 组（AC-4/AC-5）与第 3 组（AC-6/AC-7）起，采集与验收一律在 4.2.20（macOS）上进行。
- AC-4 类型对照样本改为：在 SourceTree 4.2.20 偏好设置中手动勾选对应上下文并保存后，采集写入的 `actions.plist` 条目（含字段类型）。不得用 4.2.20 样本冒充 4.2.19 样本，也不得用旧的 4.2.19 样本冒充 4.2.20 样本，不得猜测类型。
- 以下锁死点在无新证据表明 4.2.20 行为不同的前提下保持不变，但仍须用 4.2.20 样本做字节级对照后再写入：2026-11-13 冻结的九条上下文矩阵；changelog 只开日志，不开仓库、也不开文件上下文；Show Full Output 为静态开关（`pr` / `split` / `changelog` 打开，`review` / `review-branch` / `explain` 保持开，`gen` / `pr-title` / `stash-msg` 保持关；不能按成功/失败切换）；不接 `$SHA`；全部动作 `separateWindow` 保持关；参数字符串不变；已有默认快捷键不变；explain 一次只选一个文件。若 4.2.20 样本显示类型或行为与预期冲突，停止并报告，不得猜、不得改矩阵、不得软通过。
- `install` / `doctor` 的进程检测、警告并继续、doctor 核对矩阵、以及「动作全局、钩子按仓库安装」那一句，仍按 2026-11-13 锁死规则执行，并在 4.2.20 上验收。本切片不因安装路径为 `/Applications/Sourcetree.app` 而改进程名检测；仍只匹配进程名 `SourceTree`。若 4.2.20 的进程名不同，停止并报告，不得改检测方式、不得软通过。

## 范围外

Decision needed: reject a listed out item, or approve the out-list as written.

- 重开或重跑已 PASS 的 AC-2、AC-3，或回改 SHA `7d6c140` / PR #1 的第 1 组历史证明。
- 用 4.2.20 样本冒充 4.2.19，或在无 4.2.20 对照样本时猜测 `repoAction` / `fileAction` / `logAction` 类型并写入。
- 在本切片批准前采集类型样本、改机主 `actions.plist`、或开始写入文档/安装逻辑。
- 在无新证据时改冻结矩阵、打开 `separateWindow`、接 `$SHA`、把 changelog 开到仓库或文件、或改 Show Full Output 的静态开关策略。
- mergetool、apply-fixes、split --apply、新 MCP 工具，以及任何对「stai 绑定切片 2026-11-12：文本冲突 mergetool」的范围改动或推进。
- 2026-11-13 范围外已列明的其余项（提交模板、`sourcetree://`、默认 `gen --edit`、后六项默认快捷键、钩子自动改工作区或自动拆分、AI 故障阻断提交、禁止空提交、多文件 explain、新的失败弹窗通道、改成功路径的剪贴板/通知）仍为范围外。

## 验收标准

Decision needed: approve these ACs and the unlock order below.

AC-8 文档核对版本改为 4.2.20（macOS）  
Proof: SHA（改动后的 `README.md`、`README.en.md` 及被改到的注释/i18n/`docs/design.md`）+ screenshot（文档中写明核对或支持版本为 4.2.20 的表述）。  
QA: pass if 本工作相关文档不再把 SourceTree 行为或支持/核对范围锁在 4.2.19，而是写为 4.2.20（macOS）；AC-1 已通过的文案结论未回退（无「填入提交框」或 “fills in the commit box” 同类表述；主路径、打开提交框不跑钩子、Empty Commit Message、CLI 分节仍在）；未推广到未验证的 Windows 或其他版本；未改写 PR #1 / SHA `7d6c140` 上的第 1 组历史证明。

AC-4 上下文矩阵与 4.2.20 类型样本（修订 2026-11-13 的样本来源）  
Proof: fixture id `sourcetree-4220-context-type-sample`（4.2.20 偏好设置中手动勾选文件/日志/仓库上下文并保存后的 `actions.plist` 条目摘录，含字段类型）+ fixture id `install-context-matrix`（安装后九条动作的 fileAction / logAction / repoAction / separateWindow / params）+ screenshot（文件状态、文件右键、日志右键、仓库右键中出现的 stai 项与矩阵一致）。  
QA: pass if changelog 只开日志、仓库与文件均为关；其余八条与 2026-11-13 范围内表格一致；全部 `separateWindow` 为关；params 不含 `$SHA`；写入类型与本切片的 4.2.20 对照样本字节级一致；explain 文案写清一次只选一个文件；未新增默认快捷键；样本明确来自 4.2.20，不是 4.2.19 样本的重贴。

AC-5 长文打开 Show Full Output，短文成功仍无窗  
Proof: fixture id `actions-show-full-output`（九条动作的 showFullOutput 值）+ screenshot（在 4.2.20 上：`pr` / `split` / `changelog` 成功后出现输出窗口且含全文；`gen` / `pr-title` / `stash-msg` 成功后无该窗口）。  
QA: pass if 与 2026-11-13 AC-5 相同的开/关矩阵成立；短文成功无窗；未按退出码切换该开关；验收在 4.2.20 上进行。

AC-6 SourceTree 正在运行时警告并继续  
Proof: fixture id `install-doctor-sourcetree-running`、`install-doctor-sourcetree-quit`（检测所用进程名、完整警告原文、退出码）+ screenshot（在 4.2.20 仍运行时，交互式 install 的显眼警告）。  
QA: pass if 与 2026-11-13 AC-6 相同：进程名为 `SourceTree` 的进程在跑时警告并继续写入；未在跑时不发这条警告；检测只按进程名 `SourceTree`；验收在 4.2.20 上进行。

AC-7 doctor 核对矩阵并说明安装范围  
Proof: fixture id `doctor-matrix-ok`、`doctor-matrix-mismatch`（故意改错一条开关后的 doctor 输出）+ screenshot 或完整 doctor 输出文本。  
QA: pass if 与 2026-11-13 AC-7 相同，且矩阵对照的是本切片修订后的写入结果；验收在 4.2.20 上进行。

AC-2、AC-3 不在本切片重开。第 1 组 PASS 记录：SHA `7d6c140ab0cc7a41b197074f8768eacca4623e30`，[PR #1](https://github.com/ChenYiGeEr/stai/pull/1)。

## 解锁顺序

Decision needed: approve this unlock order.

本切片批准前：不得采集、不得改机主 `actions.plist`、不得开始写入，不得打开 Cloud Agent。

1. AC-8（文档版本表述）可在批准后立即开始；不重开 AC-2/AC-3。AC-8 须 PASS，本切片才算文档部分完成。
2. 然后继续 2026-11-13 第 2 组：先完成 AC-4 的 4.2.20 类型对照样本，再按矩阵写入；随后 AC-5。不得在样本完成前写入矩阵。
3. 然后第 3 组：AC-6、AC-7。

AC-8 与 AC-4/5/6/7 全部 PASS 之前，本切片不算完成，不得把未完成项软通过。本切片不解锁、不开始 mergetool 或任何范围外工作。若 4.2.20 出现与锁死点不符的新证据，停止并回到所有者出新的注明日期切片，禁止用聊天改范围。

## 交接

Decision needed: approve this handoff. Eng/QA 角色尚未命名。

- **Eng：** 切片路径为仓库外草案 `/workspace/stai-prd-cuts/2026-11-14-sourcetree-4220.md`；批准后写入 `docs/design.md` 顶部或所有者指定的切片路径，并保留 2026-11-13 切片正文不改写。日期 2026-11-14。按解锁顺序实现 AC-8，然后 AC-4（先样本后写入）→ AC-5，然后 AC-6 → AC-7。证明按各 AC 的 Proof 提交。批准前不要打开 Cloud Agent，不要采集样本，不要改机主 `actions.plist`，不要开始写入。
- **QA：** 只按本切片与仍有效的 2026-11-13 条款改写验收包；第 1 组 AC-2/AC-3 不重跑；AC-8 与修订后的 AC-4/5/6/7 按本切片验收；禁止软通过；使用同一 Proof 表。
- 所有者：批准后，hand this cut to your Eng when ready。2026-11-12 mergetool 切片仍为草案、仍暂停，不随本切片一起交给工程，除非所有者另决并先批准那一份。

---

# stai 绑定切片 2026-11-13：SourceTree 现有交互（摆放、反馈、说法）

Decision needed: approve this cut | reject a listed out item.

- 产品：stai（CLI + SourceTree 4.2.19 macOS 自定义操作与 Git 钩子）
- 日期：2026-11-13（亚洲/上海）
- 状态：已批准。日期：2026-11-13（亚洲/上海）。
- 替代：无。本切片不替代、不修改「stai 绑定切片 2026-11-12：文本冲突 mergetool」。两条谁先做由所有者之后另决。中途加范围必须再出一份注明日期的新切片，禁止用聊天改本切片或 2026-11-12 切片的范围。
- 活文档：仓库内 `docs/design.md`（本切片的 PM 审阅结论优先）；批准后将本切片置于该文档顶部（或所有者指定的切片路径）。依据审阅：`/workspace/stai-prd-cuts/2026-11-13-sourcetree-interaction-review.md`（所有者已确认，含写切片前锁死的 5 点）。

## 成功标准

Decision needed: approve this cut.

在 SourceTree 4.2.19（macOS）里，生成提交信息的主路径是 ⌥G → 通知 → 提交框 Cmd+V；文档不再写成「填入提交框」。已注册的自定义操作出现在它所服务的右键菜单里；长文（PR 描述、拆分建议、changelog）可在已有的 Show Full Output 窗口读完；自定义操作失败和钩子实际写入的提交说明都会走现有通知通道、不抢焦点。`install` / `doctor` 在 SourceTree 仍在运行时发出警告并继续，并写明动作全局、钩子按仓库安装。不自动改工作区或提交框，AI 故障不阻断提交或推送。

## 范围内

Decision needed: approve this in-list.

- 只调整已交付能力的摆放、反馈和说法。不增加命令、写入路径或功能。
- 文案：修改 `README.md`、`README.en.md`、相关钩子/命令注释与 i18n 中所有「填入提交框」及同类表述（含英文 “fills in” 等）。SourceTree 4.2.19（macOS）主路径写成 ⌥G → 通知 → 提交框 Cmd+V；写明打开提交框不会跑 `prepare-commit-msg`；空框点提交会先出系统警告「Empty Commit Message / 你没有填写提交说明」。命令行空信息提交时钩子写入 message 文件的兜底行为可如实分节保留，不得与 SourceTree 提交流程写成同一种体验。文档须写明：若用户确认空提交且钩子生成失败，仍可能留下空提交（本切片不改变该策略）。SourceTree 行为的表述限定为 4.2.19（macOS），不推广到未验证的 Windows 或旧版。
- 自定义操作失败时，通过已有通知通道（`osascript display notification`，无按钮、不抢焦点）发出已有错误句（例如暂存区为空）。成功路径不改通知通道。`gen` / `pr-title` / `stash-msg` 成功时仍无窗；失败也只用通知，不打开、不改动 Show Full Output。不得为「失败弹窗、成功不弹」增加新通道。Show Full Output 是 `actions.plist` 里每条动作的静态开关，不能按成功/失败切换。
- `prepare-commit-msg`：仅在钩子确实把生成文案写入了 message 文件之后，发一条不抢焦点的事后通知，告知「这次提交用了生成的说明」（文案随 `[global] lang` / `STAI_LANG`）。跳过时不发通知：已有文案、merge、squash、amend（`source == "commit"`）、无暂存、`STAI_DISABLE`。钩子失败可用同一通道报错，退出码仍为 0；不得在本切片里禁止空提交，也不得因 AI 故障阻断提交。
- 冻结上下文矩阵（只改已有字段；参数字符串不变；不接 `$SHA`；全部动作 `separateWindow` 保持关）。changelog **只开日志，不开仓库，也不开文件上下文**（changelog 默认是标签到 HEAD 的历史，本切片不传 `$SHA`，开仓库或文件菜单只会拉长菜单、不会给出可绑定的范围）。

  | 操作 | 参数（不变） | 文件状态/文件（fileAction） | 日志（logAction） | 仓库（repoAction） | Show Full Output |
  | --- | --- | --- | --- | --- | --- |
  | 生成提交信息 | `gen $REPO` | 开 | 关 | 开 | 关（保持） |
  | 审查改动 | `review -no-color $REPO` | 开 | 关 | 开 | 开（保持） |
  | stash 信息 | `stash-msg $REPO` | 开 | 关 | 开 | 关（保持） |
  | 拆分建议 | `split -repo=$REPO` | 开 | 关 | 开 | 开（本切片打开） |
  | 解释选中文件 | `explain -file=$FILE -repo=$REPO` | 开 | 关 | 关 | 开（保持） |
  | 审查分支 | `review-branch -no-color $REPO` | 关 | 关 | 开 | 开（保持） |
  | PR 描述 | `pr $REPO` | 关 | 关 | 开 | 开（本切片打开） |
  | PR 标题 | `pr-title $REPO` | 关 | 关 | 开 | 关（保持） |
  | changelog | `changelog -repo=$REPO` | 关 | 开 | 关 | 开（本切片打开） |

- 开/关的底层类型必须以 SourceTree 4.2.19 在偏好设置里手动勾选对应上下文并保存后写入 `actions.plist` 的样本为准，安装写入与该样本字节级一致。不得猜测 `repoAction` / `fileAction` / `logAction` 的类型（当前安装脚本把 `repoAction` 写成 Int(0)、`fileAction` / `logAction` 写成 Bool(false)，打开前必须先留下对照样本）。
- 解释选中文件：动作说明与未选中/多选时的错误提示须写清「一次只选一个文件」。不扩展为多文件。
- 已有默认快捷键不变（⌥G / ⌥R / ⌥P）；不为其余六项新增默认快捷键。向导仍可只注册部分操作；凡被注册的动作，上下文与 Show Full Output 必须符合上表。
- `install` / `doctor` 在写入或检查 `actions.plist` 时，若存在进程名为 `SourceTree` 的正在运行进程（即 SourceTree.app 的进程名；可用 `pgrep -x SourceTree` 或只匹配该进程名的同等检查，不要靠窗口标题或 plist 锁文件），则发出警告并继续写入，不拒绝安装。警告必须出现在 stdout 或 stderr；交互式 `install` 里须显眼（不得只写进日志）。进程未在跑时，不发这条警告。警告须说明：正在运行的 SourceTree 可能用内存中的动作列表覆盖刚才写入的文件，请先退出再装、装完再打开。Mac App Store 版与直装版只要进程名同为 `SourceTree`，都要警告。
- `doctor` 除原有钩子条数与动作条数外，再检查已注册 stai 动作的上下文开关与 Show Full Output 是否与上表一致；不一致须警告并列出哪一条哪一字段不符。`doctor` 输出里用一句话说清：自定义操作是全局的（一次安装对所有仓库），钩子要在每个仓库各自安装。

## 范围外

Decision needed: reject a listed out item, or approve the out-list as written.

- mergetool、apply-fixes、split --apply、新 MCP 工具，以及任何对「stai 绑定切片 2026-11-12：文本冲突 mergetool」的范围改动。
- 给 changelog / split 接 `$SHA`；写入 SourceTree 提交模板；使用 `sourcetree://`。
- 把 `gen --edit` 设成 SourceTree 默认；为后六项加默认快捷键。
- 钩子自动改工作区或自动拆分；AI 故障时阻断提交或推送；在本切片里禁止钩子失败后的空提交。
- 把 explain 做成多文件；为「失败弹窗、成功不弹」增加新通道或新窗口控制。
- 改生成类成功时的剪贴板/通知主路径；打开 `separateWindow`。

## 验收标准

Decision needed: approve these ACs and the unlock order below.

AC-1 文案与 SourceTree 4.2.19（macOS）提交流程一致  
Proof: SHA（改动后的 `README.md`、`README.en.md` 及被改到的注释/i18n）+ screenshot（文档中主路径与警告表述）。  
QA: pass if 中英文均不再出现「填入提交框」或 “fills in the commit box” 同类表述；SourceTree 节写明 ⌥G → 通知 → 提交框 Cmd+V、打开提交框不跑钩子、空框点提交先出 Empty Commit Message 警告；CLI 钩子兜底分节另述；写明确认空提交且钩子失败时可能留下空提交；未把该行为写成适用于未验证的 Windows 或旧版。

AC-2 自定义操作失败通知  
Proof: fixture id `action-fail-notify-empty-index`、`action-fail-notify-model-error`（命令、退出码、通知标题/正文、stderr 原文）；screenshot（失败通知出现且未抢焦点）。  
QA: pass if 失败时现有通知通道发出已有错误句（可被识别为同一句，例如「暂存区为空」）；`gen` / `pr-title` / `stash-msg` 失败后仍无输出窗口；成功路径通知行为与本切片前一致；未新增通知或弹窗通道。

AC-3 钩子写入后的事后通知  
Proof: fixture id `hook-notify-written`、`hook-notify-skip-existing`、`hook-notify-skip-merge`、`hook-notify-skip-amend`、`hook-notify-skip-empty-index`、`hook-notify-skip-disabled`、`hook-notify-fail-exit0`。  
QA: pass if 仅在 message 文件确实被写入生成文案后发出事后通知；所列跳过条件均不发通知；失败可发通知但进程退出 0，提交不被阻断，空提交策略未改。

AC-4 上下文矩阵与类型样本  
Proof: fixture id `sourcetree-4219-context-type-sample`（4.2.19 偏好设置中手动勾选文件/日志/仓库上下文并保存后的 `actions.plist` 条目摘录，含字段类型）+ fixture id `install-context-matrix`（安装后九条动作的 fileAction / logAction / repoAction / separateWindow / params）+ screenshot（文件状态、文件右键、日志右键、仓库右键中出现的 stai 项与矩阵一致）。  
QA: pass if changelog 只开日志、仓库与文件均为关；其余八条与范围内表格一致；全部 `separateWindow` 为关；params 不含 `$SHA`；写入类型与对照样本字节级一致；explain 文案写清一次只选一个文件；未新增默认快捷键。

AC-5 长文打开 Show Full Output，短文成功仍无窗  
Proof: fixture id `actions-show-full-output`（九条动作的 showFullOutput 值）+ screenshot（`pr` / `split` / `changelog` 成功后出现输出窗口且含全文；`gen` / `pr-title` / `stash-msg` 成功后无该窗口）。  
QA: pass if `pr` / `split` / `changelog` 为开，`review` / `review-branch` / `explain` 保持开，`gen` / `pr-title` / `stash-msg` 保持关；短文成功无窗；未按退出码切换该开关。

AC-6 SourceTree 正在运行时警告并继续  
Proof: fixture id `install-doctor-sourcetree-running`、`install-doctor-sourcetree-quit`（检测所用进程名、完整警告原文、退出码）+ screenshot（交互式 install 在 SourceTree 运行时的显眼警告）。  
QA: pass if 进程名为 `SourceTree` 的进程在跑时，`install` 与 `doctor` 都在 stdout 或 stderr 发出警告，且交互式 install 中该警告显眼；警告后安装仍继续写入，退出码不因此变为失败；进程未在跑时不发这条警告；检测只按进程名 `SourceTree`。

AC-7 doctor 核对矩阵并说明安装范围  
Proof: fixture id `doctor-matrix-ok`、`doctor-matrix-mismatch`（故意改错一条开关后的 doctor 输出）+ screenshot 或完整 doctor 输出文本。  
QA: pass if 与矩阵一致时通过或仅报告既有无关项；不一致时警告并指出动作名与字段；输出中有一句明确写出：动作是全局的，钩子要在每个仓库各自安装。

## 解锁顺序

Decision needed: approve this unlock order.

1. AC-1、AC-2、AC-3（说法与失败/钩子通知）全部 PASS 后，才能开始第 2 组。
2. AC-4、AC-5（上下文矩阵与长文输出窗口）。AC-4 的类型对照样本必须在按矩阵写入之前完成。
3. AC-6、AC-7（doctor / install 检查）。

第 1–3 组全部 PASS 之前，本切片不算完成，不得把未完成项软通过。本切片不解锁、不开始 mergetool 或任何范围外工作。

## 交接

Decision needed: approve this handoff. Eng/QA 角色尚未命名。

- **Eng：** 切片路径为仓库外草案 `/workspace/stai-prd-cuts/2026-11-13-sourcetree-interaction.md`；批准后写入 `docs/design.md` 顶部或所有者指定的切片路径。日期 2026-11-13。按解锁顺序实现 AC-1 → AC-2 → AC-3，然后 AC-4 → AC-5，然后 AC-6 → AC-7。证明按各 AC 的 Proof 提交。批准前不要打开 Cloud Agent。
- **QA：** 只按本切片改写验收包；禁止软通过；使用同一 Proof 表。
- 所有者：批准后，hand this cut to your Eng when ready。2026-11-12 mergetool 切片仍为草案、仍暂停，不随本切片一起交给工程，除非所有者另决并先批准那一份。

---

# stai 设计说明

本文记录 stai 的定位、接入方式与关键决策。使用方法见仓库根目录的 [README.md](../README.md)。

## 为什么不是插件

关键事实（无插件机制的检查记录来自当时安装的 Sourcetree 4.2.19；本工作的 SourceTree 行为核对版本是 4.2.20（macOS））：

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
| 交付顺序 | M1 commit 信息生成 → M2 提交前 review → M3-A/B/C 分支级辅助（PR 描述/标题、分支 review、stash 信息、文件解释、commit 拆分建议、changelog）→ M3 冲突合并协助（暂缓） |
| AI 辅助输出交互 | 统一「生成并复制到剪贴板 + 系统通知」：commit、PR 描述/标题、stash 信息、文件解释、拆分建议、changelog 均通过剪贴板交付，不切换窗口焦点 |
| review 增强 | 可选 `review.suggest_fixes` 为每条问题请求修改建议；strict 模式仍只阻断 high，AI 故障永不阻断 |
| PR 描述与标题 | `stai pr` 一次模型调用同时产出标题+描述（F1 契约：剪贴板=标题行+正文整块，通知副标题=标题行，标题按 Conventional Commits 校验不合规重试）；`stai pr-title` 保留专用标题提示词，服务只需一行的场景（squash merge、PR 模板仓库） |
| 分支 review 阻断策略 | 复用 M2：默认建议，pre-push 严格模式用独立 `pre_push.strict`，默认关 |
| 冲突合并中 AI 角色 | 解说员 + 逐冲突建议，AI 不直接写文件（自动解决留作进阶开关） |
| review 阻断策略 | 先建议后阻断：默认不拦截，严格模式（pre-commit 返回非零）用配置开关，默认关 |
| 配置作用域 | 双层：全局 `~/.config/stai/config.toml` + 仓库级 `.stai.toml`（可提交，团队共享约定） |
| 界面与输出语言 | `[global] lang`（严格 `zh-CN`/`en`，默认 `zh-CN`，`STAI_LANG` 可覆盖）统一驱动 CLI 文案、通知与全部 AI 提示词；替代早期的 `commit.language`（已移除）。severity 标签与 `OK` 保持英文作为解析契约，`parseReview` 双语兼容 `> 建议修改:` / `> Suggested fix:` |
