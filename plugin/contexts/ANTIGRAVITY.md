# wipnote for Antigravity

Mandatory instructions for Antigravity agents working with wipnote projects.

## Required Reading

Read the project `AGENTS.md` before using wipnote. Project-owned instruction files describe the host repo; wipnote must not create, overwrite, or silently manage those files.

## Use the CLI

Use the `wipnote` CLI for all `.wipnote/` state changes. Do not edit canonical `.wipnote/*.html` files directly.

Common workflow:

```bash
wipnote snapshot --summary
wipnote relevant <topic>
wipnote feature start <id>
wipnote check --gate --work-item <id>
wipnote feature complete <id>
```

## Antigravity Extension Integration

The generated Antigravity extension tree lives at:

```text
port/packages/antigravity-extension/
```

Development launchers should link or load that tree. Release-installed users should get the bundled tree from:

```text
~/.local/share/wipnote/antigravity-extension/
```

The `wipnote antigravity --init` flow resolves the bundled extension through wipnote shared-tree paths, not the Gemini extension path.

## Generated Assets

The Antigravity tree is generated from `packages/plugin-core/manifest.json` and shared assets under `plugin/` by:

```bash
wipnote plugin build-ports
```

Do not hand-edit generated Antigravity files unless you are also updating the source manifest or shared plugin assets that will regenerate them.
