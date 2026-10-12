---
name: stai
description: Drive the stai CLI to generate Conventional Commits messages (stai gen), run AI code review on staged changes or on the whole branch (stai review / review-branch), and produce PR title+description (stai pr / pr-title) against any target git repository. Use this skill whenever the user asks you to write or propose a commit message, review changes before committing, do a pre-commit / pre-push quality gate, summarize a branch as a PR description, or generally leans on stai for these — even if they don't say "stai" but say things like "帮我写个提交信息", "提交前审查一下", "生成 PR 描述", "commit message", "review my staged changes", or "draft a PR for this branch".
---

# stai — AI commit / review / PR workflow

stai is a local CLI that reads git diffs and calls an OpenAI-compatible LLM to
produce commit messages, code review reports, and PR text. You drive it with
plain shell commands against the target repository; the target project does not
need to have stai installed (hooks are optional), only the current machine does.

## Preconditions (check once, cheaply)

1. `stai` is on PATH. If not, check for a local source tree of stai and run
   `./install.sh`, or ask the user to install it.
2. `~/.config/stai/config.toml` exists and its provider URL is reachable
   (default is a local Ollama at `http://localhost:11434/v1`). If the LLM call
   fails with a connection error, tell the user to start their model service;
   do not retry blindly and never invent API keys.

Every command below takes the target repo as an optional positional argument.
When empty it uses the current working directory, so prefer passing the repo
path explicitly: the user's CWD may not be the target project.

## Task → command mapping

### Generate a commit message

```
stai gen <repo>
```

Reads the **staged** diff only. Prints the message to stdout and copies it to
the clipboard. If staged is empty, it errors out — stage changes with
`git add` first. Tell the user the commit message is also on their clipboard if
they are pasting it into a GUI like SourceTree.

### Code review before committing

```
stai review <repo>
```

Reviews the **staged** diff and prints a severity-annotated report.
Exit codes matter for automation:

- `0` — review completed (findings may still exist; no hard failure)
- `1` — review was incomplete (model error partway) or blocked by `--strict`
- `2` — staged diff is empty (nothing to review)

Agent-useful flags:

- `--strict` — exit 1 when any high-severity finding exists. Use when the user
  asks for a blocking review / quality gate. Note the **double dash**; `-strict`
  is not recognized by stai.
- `--format json --output <file>` — machine-readable findings written to a
  file; prefer this when you need to parse or fix findings programmatically.
- `--no-cache` — bypass the local result cache; use when the user suspects a time-out or the findings look out of sync.
- `--no-color` — strip ANSI escapes; use when capturing output to a file.

### Review the whole branch before pushing / opening a PR

```
stai review-branch <repo>
```

Reviews the diff of the current branch against `pre_push.base_ref`
(default `origin/main`). Same flags and exit codes as `stai review`.

### Draft a PR (title + description)

```
stai pr <repo>
```

Reads the branch diff against `pre_push.base_ref` and produces a Conventional
Commits-compliant title on line 1, then an empty line, then the description
(sections `## 摘要` / `## 主要改动` when the output language is Chinese).
The full block is also copied to the clipboard — line 1 goes into the PR title
field, the rest into the body. For just the title: `stai pr-title <repo>`.

If `stai pr` fails because `origin/main` (the default `pre_push.base_ref`) does
not exist, override the base ref for this repo by creating a `.stai.toml` file
at the repo root:

```toml
[pre_push]
base_ref = "main"
```

Then rerun `stai pr <repo>`. Tell the user you created `.stai.toml` so they can
commit it if they want the setting to persist. Only add a temporary remote as a
fallback, and remove it immediately after the stai command finishes.

## Behavior notes that affect how you present results

- **Clipboard + system-notification side effects.** On macOS every review also
  fires a system notification and writes PR/gen output to the clipboard. When
  presenting results, tell the user what is on their clipboard so they don't
  get blind-sided by it.
- **Incomplete reviews.** A review can return partial findings plus an error
  (partial groups failed at the provider). The report is then framed with an
  "incomplete" header/footer and exit code is 1. Do not treat partial findings
  as the full result — report the error, show what was found, and suggest a
  rerun (cache means transient failures usually don't re-cost).
- **Language.** Output language comes from config `global.lang` (default
  `zh-CN`). Override per run with `STAI_LANG=en` if the user wants English PR
  text for an English-speaking team.
- **`--strict` vs review config.** `review.strict` / `pre_push.strict` in the
  config make the block behavior permanent; the `--strict` flag enables the
  same blocking for a single run without changing config.
- **Strict blocking is advisory for agents.** Never `git commit` or `git push`
  for the user because stai approved it — the user still decides. Present
  stai's report and the high-severity findings, apply fixes with confirmation.

## Common pitfalls

- **Wrong flag syntax.** stai uses GNU-style long flags with two dashes:
  `--strict`, `--no-cache`, `--no-color`. A single dash like `-strict` is
  ignored and the review runs non-blocking.
- **Empty staged diff.** `stai gen` and `stai review` read staged changes only.
  If the user sees exit code 2, tell them to `git add` first.
- **Missing base ref for PR / review-branch.** The default base is `origin/main`.
  If the repo has no `origin/main`, create `.stai.toml` with

  ```toml
  [pre_push]
  base_ref = "main"
  ```

  (or the appropriate branch) and rerun. Do not leave temporary remotes behind.

## Adjacent commands (mention only when relevant)

- `stai split <repo>` — suggest how to split the staged diff into logical commits.
- `stai stash-msg <repo>` — one-line stash message from the working-tree diff.
- `stai explain <repo> -file=<f>` — 2-5 sentence explanation of a file.
- `stai changelog [since]` — categorized changelog since a tag/ref.
