# wipnote Installation Guide

## Prerequisites

- Go 1.21+ (for building from source)
- Git

---

## Install the CLI

```bash
# Universal installer (recommended)
curl -fsSL https://raw.githubusercontent.com/shakestzd/wipnote/main/install.sh | sh

# Or build from source
git clone https://github.com/shakestzd/wipnote.git
cd wipnote && go build -o ~/.local/bin/wipnote ./cmd/wipnote/
```

### Upgrading

```bash
wipnote upgrade            # latest release
wipnote upgrade --check    # check without installing
```

---

## Claude Code Integration

Install the wipnote plugin from the Claude Code marketplace:

```bash
wipnote claude --init     # registers the marketplace and installs the plugin
wipnote claude            # launch Claude Code with wipnote context
```

### Dev mode (dogfooding from source)

```bash
wipnote claude --dev      # links local plugin source and launches Claude Code
```

### Resume sessions

```bash
wipnote claude --continue              # resume the last session
wipnote claude --resume <session-id>   # resume a specific session by UUID
```

---

## Antigravity Integration

> Antigravity supersedes the Gemini CLI harness, which was retired as a launch/generation
> target (feat-02f25a24). wipnote still reads historical Gemini CLI session data (the
> ingest/classification read path is retained), but `wipnote gemini` is no longer a command.

The wipnote Antigravity extension is bundled with each release tarball
(`antigravity-extension/`, installed to `~/.local/share/wipnote/antigravity-extension/`)
and installed locally via the `agy` CLI's own plugin mechanism — there is no separate
git-ref/tag download step.

### Install

```bash
wipnote antigravity --init          # installs the bundled extension via `agy plugin install`
wipnote antigravity                 # launch Antigravity with wipnote context
```

```bash
wipnote antigravity --init --force  # reinstall over an existing install
```

### Resume sessions

```bash
wipnote antigravity --continue          # agy --continue (resume the most recent conversation)
wipnote antigravity --resume <id>       # agy --conversation <id> (resume a specific conversation)
```

### Dev mode (dogfooding from source)

```bash
wipnote antigravity --dev               # links port/packages/antigravity-extension/ and launches
```

Dev mode links the in-tree `port/packages/antigravity-extension/` (idempotent) before
launching, so changes to the generated tree are picked up without reinstalling. Regenerate
it with `wipnote plugin build-ports --target antigravity` after editing shared `plugin/`
assets or `packages/plugin-core/manifest.json`.

---

## Codex CLI Integration

```bash
wipnote codex --init      # registers the wipnote Codex marketplace
wipnote codex             # launch Codex CLI with wipnote context
```

### Resume sessions

```bash
wipnote codex --continue             # codex resume --last
wipnote codex --resume <session-id>  # codex resume <id>
```

### Dev mode

```bash
wipnote codex --dev       # registers packages/codex-marketplace/ locally and launches Codex
```

---

## Initialize in a project

After installing the CLI and at least one AI tool integration:

```bash
cd /your/project
wipnote init              # creates .wipnote/ and installs hooks
```

---

## Verify installation

```bash
wipnote version           # prints version information
wipnote status            # project health overview
wipnote serve             # starts the local dashboard at localhost:4000
```
