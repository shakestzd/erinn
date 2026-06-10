# gemini-extension — Generated Gemini CLI extension tree

## Installing the extension (end users)

The extension is distributed via a CI-maintained split branch. Install by pinning to a
versioned tag published on every release:

```
gemini extensions install shakestzd/wipnote --ref gemini-extension-v<version>
```

For example, using the latest release:

```
gemini extensions install shakestzd/wipnote --ref gemini-extension-v0.55.5
```

The tag `gemini-extension-vX.Y.Z` points to a branch root that contains **only** the
extension tree (no other monorepo paths). This is required because `gemini extensions install`
has no subpath or sparse-checkout flag — the extension manifest must live at the repo root at
the referenced ref.

The distribution branch `gemini-extension-dist` and per-release tags are maintained
automatically by the CI workflow `.github/workflows/gemini-subtree.yml` on every GitHub
release. You do not need to manage this branch manually.

---

**This tree is generated from `packages/plugin-core/`. Do not hand-edit.**

Regenerate with:

    wipnote plugin build-ports --target gemini

Any change here is a change to the build output and will be overwritten on the
next run. To add commands, agents, skills, or hooks, edit the shared source
under `plugin/` and `packages/plugin-core/manifest.json` — see
[`packages/plugin-core/README.md`](../plugin-core/README.md) for the per-task
recipes (new command, new agent, new skill, new hook).

## Install for local testing

Link this tree into your Gemini CLI and restart so the new extension is picked up:

    gemini extensions link $(pwd)/port/packages/gemini-extension

Then restart the Gemini CLI. Unlink with `gemini extensions unlink wipnote`
when you're done.

## Tree layout

    port/packages/gemini-extension/
    ├── gemini-extension.json     # extension manifest (name, version, contextFileName)
    ├── GEMINI.md                 # context file copied from the repo root
    ├── commands/<namespace>/     # TOML slash commands (translated from plugin/commands/*.md)
    ├── agents/                   # markdown agent definitions (copied verbatim)
    ├── skills/<name>/SKILL.md    # skill directories (copied verbatim)
    └── hooks/hooks.json          # hook event wiring for Gemini-targeted events

See `port/pluginbuild/gemini.go` for the emitter and the sub-emitter files
(`gemini_*.go`) that populate each part of the tree.
