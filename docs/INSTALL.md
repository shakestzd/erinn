# HtmlGraph Installation Guide

## Prerequisites

- Go 1.21+ (for building from source)
- Git

---

## Install the CLI

```bash
# Universal installer (recommended)
curl -fsSL https://raw.githubusercontent.com/shakestzd/htmlgraph/main/install.sh | sh

# Or build from source
git clone https://github.com/shakestzd/htmlgraph.git
cd htmlgraph && go build -o ~/.local/bin/htmlgraph ./cmd/htmlgraph/
```

### Upgrading

```bash
htmlgraph upgrade            # latest release
htmlgraph upgrade --check    # check without installing
```

---

## Claude Code Integration

Install the HtmlGraph plugin from the Claude Code marketplace:

```bash
htmlgraph claude --init     # registers the marketplace and installs the plugin
htmlgraph claude            # launch Claude Code with HtmlGraph context
```

### Dev mode (dogfooding from source)

```bash
htmlgraph claude --dev      # links local plugin source and launches Claude Code
```

### Resume sessions

```bash
htmlgraph claude --continue              # resume the last session
htmlgraph claude --resume <session-id>   # resume a specific session by UUID
```

---

## Gemini CLI Integration

The HtmlGraph Gemini extension is distributed via the `gemini-extension-dist` branch of
this repository, published automatically on every release as a `gemini-extension-v<version>`
tag.

### Install

```bash
htmlgraph gemini --init     # installs the extension matching the htmlgraph binary version
htmlgraph gemini            # launch Gemini CLI with HtmlGraph context
```

The `--init` command runs:

```
gemini extensions install shakestzd/htmlgraph --ref gemini-extension-v<version> --consent --skip-settings
```

Where `<version>` matches the currently installed `htmlgraph` binary. Pass `--ref` to
override:

```bash
htmlgraph gemini --init --ref gemini-extension-v0.55.6   # pin a specific version
htmlgraph gemini --init --force                          # reinstall over existing
```

### Resume sessions

Gemini uses session **indices** (integers), not UUIDs. List sessions to find the index:

```bash
htmlgraph gemini --list-sessions    # gemini --list-sessions
htmlgraph gemini --continue         # gemini --resume latest
htmlgraph gemini --resume 3         # gemini --resume 3
```

### Dev mode (dogfooding from source)

```bash
htmlgraph gemini --dev              # links packages/gemini-extension/ as a live pointer
htmlgraph gemini --dev --isolate    # also passes -e htmlgraph to suppress other extensions
```

Dev mode runs `gemini extensions link /abs/path/to/packages/gemini-extension` (idempotent)
before launching. The live link means changes to `packages/gemini-extension/` are picked
up immediately without reinstalling.

---

## Codex CLI Integration

```bash
htmlgraph codex --init      # registers the HtmlGraph Codex marketplace
htmlgraph codex             # launch Codex CLI with HtmlGraph context
```

### Resume sessions

```bash
htmlgraph codex --continue             # codex resume --last
htmlgraph codex --resume <session-id>  # codex resume <id>
```

### Dev mode

```bash
htmlgraph codex --dev       # registers packages/codex-marketplace/ locally and launches Codex
```

---

## Initialize in a project

After installing the CLI and at least one AI tool integration:

```bash
cd /your/project
htmlgraph init              # creates .htmlgraph/ and installs hooks
```

---

## Verify installation

```bash
htmlgraph version           # prints version information
htmlgraph status            # project health overview
htmlgraph serve             # starts the local dashboard at localhost:4000
```
