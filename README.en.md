# stai — AI Companion for SourceTree

[中文](README.md) | English

stai is a standalone CLI that plugs into SourceTree workflows through standard
git mechanisms (hooks, custom actions). It is not a SourceTree plugin
(SourceTree has no plugin API — see [docs/design.md](docs/design.md)).

## UI language

The language of CLI text, system notifications and AI output (commit
messages, PR descriptions, review reports, ...) is controlled by
`[global] lang`: `zh-CN` (default) or `en`. The `STAI_LANG` environment
variable overrides it temporarily.

## Feature status

| Feature | Command | Status |
| --- | --- | --- |
| Commit message generation | `stai gen`, `stai hook prepare-commit-msg`, `stai install` | ✅ Available (M1) |
| Pre-commit review | `stai review`, `stai hook pre-commit`, `stai install` | ✅ Available (M2) |
| PR description generation | `stai pr` | ✅ Available (M3-A) |
| PR title generation | `stai pr-title` | ✅ Available (M3-C) |
| Branch review | `stai review-branch`, `stai hook pre-push` | ✅ Available (M3-B) |
| Stash message generation | `stai stash-msg` | ✅ Available (M3-C) |
| File explanation | `stai explain <file>` | ✅ Available (M3-C) |
| Commit split suggestion | `stai split` | ✅ Available (M3-C) |
| Changelog generation | `stai changelog [since]` | ✅ Available (M3-C) |
| Conflict resolution assist | `stai mergetool` | 🚧 Not implemented (M3) — prints a hint only |

## Prerequisites

- macOS (the SourceTree integration relies on `osascript`, `pbcopy` and
  SourceTree's `actions.plist`);
- SourceTree (verified on 4.2.19);
- Go 1.23+ (only for building from source);
- An OpenAI-compatible model endpoint — local Ollama by default
  (`http://localhost:11434/v1`), LM Studio or API gateways work too.

## Installation

Run in a repository root (after a global install it works in any repo):

```bash
stai install                  # write the three hooks + register nine SourceTree custom actions
stai install -no-sourcetree   # hooks only, leave SourceTree alone
stai uninstall                # remove everything install wrote (stai-owned entries only)
```

`install` does the following:

1. Writes a `prepare-commit-msg` hook into the current repo (generates a
   message when the commit box is left empty; never blocks a commit);
2. Writes a `pre-commit` hook (reviews and blocks only when
   `review.strict = true`; passes otherwise);
3. Writes a `pre-push` hook (reviews the branch diff and blocks the push
   only when `pre_push.strict = true`; passes otherwise);
4. Registers nine custom actions in SourceTree's action storage,
   `~/Library/Application Support/SourceTree/actions.plist`. The default
   menu captions are Chinese (fixed defaults, independent of `global.lang`;
   override them via the `sourcetree.*_caption` keys):
   - **AI 生成提交信息**, params `gen $REPO`, shortcut **⌥G**;
   - **AI 审查改动**, params `review $REPO`, shortcut **⌥R**;
   - **AI 生成 PR 描述**, params `pr $REPO`, shortcut **⌥P**;
   - **AI 生成 PR 标题**, params `pr-title $REPO`, no shortcut;
   - **AI 审查分支**, params `review-branch $REPO`, no shortcut;
   - **AI 生成 stash 信息**, params `stash-msg $REPO`, no shortcut;
   - **AI 解释选中文件**, params `explain -file=$FILE -repo=$REPO`, no
     shortcut (requires a selected file);
   - **AI 拆分 commit 建议**, params `split -repo=$REPO`, no shortcut;
   - **AI 生成 changelog**, params `changelog -repo=$REPO`, no shortcut.

   (`$REPO` is expanded by SourceTree to the repository path; shortcuts can
   be changed in SourceTree → Settings → Custom Actions.)

Both `install` and `uninstall` are idempotent: install replaces old entries
pointing at the same binary or carrying the same captions; uninstall removes
only hooks marked `installed by stai` and stai's action entries — anything
created by other tools is preserved.

> **Note**: quit SourceTree before re-running `install`. While running,
> SourceTree overwrites `actions.plist` from its in-memory list, which
> discards the registration. Restart SourceTree afterwards to see the menu
> items.

## Usage

### Custom actions (recommended)

Stage your changes in SourceTree, then run
**Actions → Custom Actions → AI 生成提交信息** (or press ⌥G):

1. The message is copied to the clipboard and shown in a system notification
   (SourceTree discards custom-action stdout — without the notification you
   would see nothing);
2. Click into the commit box and press Cmd+V;
3. Review, edit, and commit.

The window focus never changes; nothing is written into the commit box
without your confirmation, and your existing draft is never overwritten.

CLI equivalent: `stai gen`; `stai gen --edit` first shows an editable dialog
and copies only after confirmation.

### Hook fallback (zero actions)

Commit with an empty message box and the `prepare-commit-msg` hook fills in
a generated message automatically.

It skips: merge / squash / `commit --amend` style commits, and commits that
already carry a message. Generation failures print a warning and never block
the commit (a 120-second internal timeout caps the wait).

### Pre-commit review (M2)

Stage your changes, then run **Actions → Custom Actions → AI 审查改动**
(or press ⌥R):

- The report prints to stdout (CLI) or shows up in a system notification
  (SourceTree);
- Both review actions (**AI 审查改动**, **AI 审查分支**) enable
  "Show Full Output", so SourceTree pops up the output window with the full
  report;
- Severity labels are uppercase `[HIGH]` / `[MEDIUM]` / `[LOW]`;
- With `review.suggest_fixes` (default `true`) each finding is followed by a
  `> Suggested fix: ...` line; suggestions appear on stdout and in the
  report file, while notifications stay concise;
- SourceTree actions run with `-no-color` (plain text in the output window);
  run `stai review` / `stai review-branch` in a terminal for colored output;
- Notifications and the report file stay plain text;
- When changed lines exceed `review.group_max_lines` (default 100), the diff
  is split into per-file groups, reviewed group by group, then merged and
  sorted (high → low). Small models miss issues in large diffs that dilute
  their attention; per-file review mitigates this (grouping design borrowed
  from alibaba/open-code-review);
- The review prompt embeds two core principles (precision beats recall; no
  findings deterministic tooling already surfaces) plus a condensed Go
  checklist (injected when the diff touches `.go` files); `review.rules`
  appends project-specific rules;
- When findings exceed `review.notify_max_findings` (default 5), the
  notification shows a per-severity summary and the full report goes to
  `review.report_path` (default `.git/stai-review.md`), whose path is
  included in the notification;
- With no findings the notification shows "OK — no issues found";
- review can use a stronger model via `review.base_url` / `review.model`
  while gen stays on the fast local one (see the config table).

CLI equivalent: `stai review`; `stai review -strict` enables strict mode for
this run (exit code 1 on high-severity findings, scriptable).

### Branch-level helpers (M3-A/B/C)

#### PR description (M3-A)

Run **Actions → Custom Actions → AI 生成 PR 描述** (or press ⌥P):

- Reads the diff of the current branch against `pre_push.base_ref`
  (default `origin/main`);
- Produces the PR title and description in a SINGLE model call: the
  clipboard holds the combined block (title line, blank line, then the body
  with `## Summary` and `## Main changes`), and the notification subtitle
  shows the title line;
- The title is validated as a Conventional Commits subject (auto-retried on
  violation) — cut the first line into the title field and paste the rest as
  the PR body on GitHub/GitLab.

CLI equivalent: `stai pr`. Use `stai pr-title` when you only want the
one-line title.

#### Branch review (M3-B)

Run **Actions → Custom Actions → AI 审查分支** (menu only, no default
shortcut):

- Reviews the full branch diff against `pre_push.base_ref`;
- Everything else matches `stai review`: report, notification, report file,
  severity summary.

CLI equivalent: `stai review-branch`; `stai review-branch -strict` for a
strict run.

With `pre_push.strict = true`, `git push` triggers a branch review
automatically: **high**-severity findings block the push, everything else
passes. Off by default; AI or parse failures never block a push.
Bypass: `STAI_DISABLE=1 git push ...` or `git push --no-verify`.

#### Stash message (M3-C)

Run **Actions → Custom Actions → AI 生成 stash 信息**:

- Reads the working-tree changes vs HEAD (staged + unstaged tracked files)
  and generates a one-line description;
- Copies it to the clipboard — paste into SourceTree's Stash dialog.

CLI equivalent: `stai stash-msg`.

#### PR title (M3-C)

Run **Actions → Custom Actions → AI 生成 PR 标题**:

- Reads the diff of the current branch against `pre_push.base_ref`;
- Generates a one-line Conventional Commits style title and copies it to the
  clipboard;
- Paste into the PR title field on GitHub/GitLab.

CLI equivalent: `stai pr-title`.

#### File explanation (M3-C)

Select a file in SourceTree, then run
**Actions → Custom Actions → AI 解释选中文件**:

- Reads the selected file and explains its responsibility, key structure and
  caveats in 2-5 sentences;
- Copies the explanation to the clipboard.

CLI equivalent: `stai explain <file>`.

#### Commit split suggestion (M3-C)

Run **Actions → Custom Actions → AI 拆分 commit 建议**:

- Reads the staged diff and suggests how to split it into logical commits;
- Each proposed commit comes with its file list; copied to the clipboard.

CLI equivalent: `stai split`.

#### Changelog (M3-C)

Run **Actions → Custom Actions → AI 生成 changelog**:

- By default reads the history from the most recent tag
  (`git describe --tags --abbrev=0`) to HEAD;
- Generates a categorized markdown changelog and copies it to the clipboard;
- Pass a range explicitly: `stai changelog v1.0.0`.

With `review.strict = true` (global or repo config), every `git commit` is
reviewed before it lands:

- **high**-severity findings block the commit and the report shows in the
  commit output;
- no high findings, model errors, or unparseable output all pass (AI
  failures never block a commit);
- bypass: `STAI_DISABLE=1 git commit ...` or `git commit --no-verify`.

Off by default. Run in advisory mode (⌥R / `stai review`) for a while and
check the false-positive rate on your project before enabling it.

> Tip: review quality depends on the model. Use a small local model for gen
> (seconds) and point `review.model` at a stronger one; with a small local
> model, lowering `review.group_max_lines` (e.g. 50) improves recall at the
> cost of more serial calls and longer waits. review-branch inherits the
> same `review.*` model and threshold settings.

Disable hooks temporarily (without uninstalling):
`STAI_DISABLE=1 git commit ...` or `STAI_DISABLE=1 git push ...`

## Configuration

Configuration is merged in this order, later winning:

1. Built-in defaults
2. Global config `~/.config/stai/config.toml`
3. Repo config `.stai.toml` (in the repo root; committable for team-wide
   conventions)
4. Environment variables (highest priority, for temporary overrides)

Every key has a default; the example below is a complete starting point:

```toml
[global]
lang = "zh-CN"            # UI and AI output language: "zh-CN" or "en"

[provider]
base_url        = "http://localhost:11434/v1"
api_key         = ""
model           = "qwen2.5-coder:7b"
timeout_seconds = 120

[commit]
style            = "conventional"
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
concurrency = 4                                  # parallel group reviews; remote providers benefit, a local single GPU queues, 1 = serial
suggest_fixes = true                             # ask the model for a concrete fix per finding
# rules = ["every error in a new function must be logf'd or returned"]  # project-specific review rules
# base_url = "" / api_key = "" / model = ""        # review-only model overrides; empty inherits [provider]
# temperature = 1                                  # set when the review model only accepts temperature=1

[hook]
timeout_seconds = 120

[pre_push]
base_ref = "origin/main"  # comparison baseline for PR descriptions, branch review and the pre-push hook
strict = false            # true: review the branch diff before git push and block on high findings

[notify]
title    = "stai"
subtitle = ""  # empty = pick the zh/en default text automatically

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

Reference:

Note: the `sourcetree.*_caption` defaults are Chinese menu captions — they are
fixed config defaults and do NOT follow `global.lang`; set them explicitly if
you want English menu items.

| Key | Default | Description |
| --- | --- | --- |
| `global.lang` | `zh-CN` | Language of CLI text, notifications and AI output; strictly `zh-CN` or `en`; invalid values fall back to `zh-CN` with one warning |
| `provider.base_url` | `http://localhost:11434/v1` | OpenAI-compatible endpoint (Ollama, LM Studio, gateways) |
| `provider.api_key` | `""` | API key; empty for local models |
| `provider.model` | `qwen2.5-coder:7b` | Model name |
| `provider.timeout_seconds` | `120` | Per-request timeout (seconds) |
| `provider.temperature` | `0` | Request temperature, `0` = deterministic; some models only accept `1` |
| `commit.style` | `conventional` | Only `conventional` is supported; other values error out |
| `commit.types` | `feat` … `ci` (10 total) | Allowed types; out-of-list output is retried, then errors |
| `commit.subject_max` | `50` | Subject length hint (characters) given to the model |
| `commit.body_min_lines` | `100` | Write a body only above this many changed lines |
| `commit.body_min_files` | `3` | … or above this many touched files |
| `commit.body_max_items` | `5` | Max body bullets |
| `commit.max_diff_chars` | `60000` | Diffs longer than this are truncated before sending |
| `commit.retries` | `1` | Retries after a non-conforming reply |
| `review.strict` | `false` | `true`: the pre-commit hook blocks on high-severity findings (see "Strict mode") |
| `review.notify_max_findings` | `5` | Above this many findings the notification shows a summary and the full report goes to the report file |
| `review.report_path` | `.git/stai-review.md` | Full report location (relative to the repo root) |
| `review.group_max_lines` | `100` | Changed lines above this split the diff into per-file review groups (open-code-review's grouping threshold) |
| `review.concurrency` | `4` | Parallel group reviews; remote providers cut wall time, a local single GPU queues, `1` = serial |
| `review.suggest_fixes` | `true` | Ask the model for a concrete fix per finding, shown on stdout and in the report file; notifications stay summary-only |
| `review.rules` | `[]` | Project-specific review rules appended verbatim to the review prompt |
| `review.base_url` / `review.api_key` / `review.model` | inherit `[provider]` | Review-only model overrides; lets review use a big model while gen stays on the fast local one |
| `review.temperature` | inherit `provider.temperature` | Set only when the review model restricts temperature (e.g. reasoning models that accept only `1`) |
| `hook.timeout_seconds` | `120` | Overall hook timeout (seconds) for `prepare-commit-msg`, `pre-commit` and `pre-push` |
| `pre_push.base_ref` | `"origin/main"` | Comparison baseline for PR descriptions, branch review and the pre-push hook |
| `pre_push.strict` | `false` | `true`: review the branch diff before `git push` and block on high findings |
| `notify.title` | `stai` | System notification title |
| `notify.subtitle` | `""` (empty = language-dependent default) | Notification subtitle for generated commit messages |
| `sourcetree.action_caption` | `AI 生成提交信息` | Menu caption of the commit-message action; re-run `stai install` after changing |
| `sourcetree.shortcut_key_code` | `5` | Shortcut key code, `5` = G |
| `sourcetree.shortcut_modifiers` | `524288` | Modifier flags, `524288` = Option (⌥) |
| `sourcetree.shortcut_display` | `⌥G` | Shortcut display text |
| `sourcetree.review_action_caption` | `AI 审查改动` | Menu caption of the review action; re-run `stai install` after changing |
| `sourcetree.review_shortcut_key_code` | `15` | Review shortcut key code, `15` = R |
| `sourcetree.review_shortcut_modifiers` | `524288` | Modifier flags, `524288` = Option (⌥) |
| `sourcetree.review_shortcut_display` | `⌥R` | Review shortcut display text |
| `sourcetree.pr_action_caption` | `AI 生成 PR 描述` | Menu caption of the PR-description action |
| `sourcetree.pr_shortcut_key_code` | `35` | PR shortcut key code, `35` = P |
| `sourcetree.pr_shortcut_modifiers` | `524288` | Modifier flags, `524288` = Option (⌥) |
| `sourcetree.pr_shortcut_display` | `⌥P` | PR shortcut display text |
| `sourcetree.review_branch_action_caption` | `AI 审查分支` | Menu caption of the branch-review action |
| `sourcetree.stash_msg_action_caption` | `AI 生成 stash 信息` | Menu caption of the stash-message action |
| `sourcetree.pr_title_action_caption` | `AI 生成 PR 标题` | Menu caption of the PR-title action |
| `sourcetree.explain_action_caption` | `AI 解释选中文件` | Menu caption of the file-explain action |
| `sourcetree.split_action_caption` | `AI 拆分 commit 建议` | Menu caption of the commit-split action |
| `sourcetree.changelog_action_caption` | `AI 生成 changelog` | Menu caption of the changelog action |
| `log.path` | `~/Library/Logs/stai.log` | Diagnostic log: every `gen` / `review` / `pr` / hook run and clipboard result |

A repo-level `.stai.toml` uses the same structure; write only the keys you
want to override.

### Environment variables

| Variable | Effect |
| --- | --- |
| `STAI_BASE_URL` | Overrides `provider.base_url` |
| `STAI_API_KEY` | Overrides `provider.api_key` |
| `STAI_MODEL` | Overrides `provider.model` |
| `STAI_LANG` | Overrides `global.lang` (`zh-CN` or `en`) |
| `STAI_DISABLE` | When non-empty, hooks pass through without generating anything |

## Development

```bash
go build ./...                 # build
go test ./...                  # tests
go run ./cmd/stai help         # command help
```

## Layout

```
cmd/stai/        CLI entrypoint and subcommands (gen / hook / review / pr / pr-title / review-branch / stash-msg / explain / split / changelog / mergetool / install / uninstall)
internal/ai      OpenAI-compatible client; commit/PR-title/PR-description/stash/explain/split/changelog generation and staged/branch review (bilingual prompts)
internal/config  layered configuration loading
internal/git     git command wrappers (staged diff, branch diff, working-tree diff, git log, tags, hook paths)
internal/i18n    zh/en CLI string tables and language switching
docs/            design notes (design.md)
```

## Roadmap

- **M1 — commit message generation** (done): `stai gen` (generate & copy),
  `stai hook prepare-commit-msg` (empty-box fallback), `stai install`.
  Acceptance: full flow works inside SourceTree without leaving the GUI.
- **M2 — pre-commit review** (done): `stai review` (⌥R custom action,
  advisory mode) + `stai hook pre-commit` (`review.strict` gate) +
  `stai uninstall`.
  Acceptance: ⌥R shows a report in SourceTree; strict mode blocks commits on
  high-severity findings; AI failures never block.
- **M3-A/B/C — branch-level helpers** (done): `stai pr` (PR description),
  `stai pr-title` (PR title), `stai review-branch` / `stai hook pre-push`
  (branch review), `stai stash-msg` (stash message), `stai explain` (file
  explanation), `stai split` (commit split suggestion), `stai changelog`
  (changelog generation).
  Acceptance: ⌥P generates a PR description in SourceTree; review-branch
  reuses M2's review pipeline on branch diffs via action and hook; all new
  actions deliver via the clipboard without changing window focus.
- **M3 — conflict resolution assist**: `stai mergetool` wired into
  `git config mergetool`, offering per-hunk "explain + recommend +
  accept/edit yourself" interaction.
