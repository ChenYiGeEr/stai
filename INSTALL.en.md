# Install stai

One install. AI assistance for your SourceTree git workflow.

## One-liner

**macOS**

```bash
curl -fsSL https://raw.githubusercontent.com/chenyiGeEr/stai/v0.1.0/install.sh | bash
```

> Piping a script straight into a shell runs it sight-unseen. If you'd rather read it first, download then run:
>
> ```bash
> curl -fsSL https://raw.githubusercontent.com/chenyiGeEr/stai/v0.1.0/install.sh -o install.sh
> # review install.sh
> bash install.sh
> ```
>
> Both the bootstrap script and the `go install` target are pinned to `v0.1.0`, never the moving `main` branch.

What it does:

- Checks that Go 1.23+ is installed (stai needs the Go toolchain);
- Auto-detects a writable directory already in your PATH (prefers `~/.local/bin`, then `~/bin`, then `/usr/local/bin`);
- Installs the `stai` binary via `go install github.com/chenyiGeEr/stai/cmd/stai@v0.1.0`;
- **Does not modify the current repository** and **does not touch SourceTree**.

After installation, run `stai install` from any git repository root to set up hooks and SourceTree custom actions.

Want to preview before installing? Use `--dry-run`:

```bash
curl -fsSL https://raw.githubusercontent.com/chenyiGeEr/stai/v0.1.0/install.sh | bash -s -- --dry-run
```

## Install options

| Method | Command | When to use |
|---|---|---|
| **One-liner (recommended)** | `curl -fsSL .../install.sh \| bash` | Quick install; requires Go |
| **`go install`** | `go install github.com/chenyiGeEr/stai/cmd/stai@latest` | You already use the Go toolchain |
| **Build from source** | `git clone ... && cd stai && go build -o stai ./cmd/stai` | You want to modify or debug |

> If you use `go install` or build from source, place the resulting `stai` binary in a directory on your PATH (e.g. `~/.local/bin`).

## Prerequisites

- **macOS** (the SourceTree integration relies on `osascript`, `pbcopy`, and SourceTree's `actions.plist`);
- **SourceTree** (verified on 4.2.19);
- **Go 1.23+** (required by the one-liner, `go install`, and source builds);
- An **OpenAI-compatible model endpoint** — local Ollama by default (`http://localhost:11434/v1`), LM Studio or API gateways work too.

## Build from source

```bash
git clone https://github.com/chenyiGeEr/stai.git
cd stai
go build -o stai ./cmd/stai

# Optional: copy to PATH
mkdir -p ~/.local/bin
cp stai ~/.local/bin/
```

## Configure and use

Run from a repository root:

```bash
stai install                  # write the three hooks + register nine SourceTree custom actions
stai install -no-sourcetree   # hooks only, leave SourceTree alone
stai uninstall                # remove everything install wrote (stai-owned entries only)
```

The first time you run `stai install` and `~/.config/stai/config.toml` does not exist, an interactive wizard will ask for language, provider base URL/API key, model, and which SourceTree custom actions to register.

For detailed usage, see [README.en.md](README.en.md).

## Verify the installation

```bash
stai version
stai doctor
```

`stai version` should print `v0.1.0`.

`stai doctor` checks:

- whether the global config file exists;
- whether the model endpoint is reachable;
- whether the current directory is a git repository;
- whether the `prepare-commit-msg` / `pre-commit` / `pre-push` hooks are installed;
- how many SourceTree custom actions are registered;
- whether the log file is writable.

If `stai version` fails with "command not found", make sure the install directory (e.g. `~/.local/bin`) is on your PATH.
