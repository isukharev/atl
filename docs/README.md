# `atl` documentation

Start with the project [README](../README.md) for install, quick start, and the Claude Code plugin.
This directory holds the deeper references:

| Doc | Audience | What's in it |
|-----|----------|--------------|
| [usage.md](usage.md) | **Users / scripts** | Full command reference, global conventions, exit codes, environment variables, and a **Scripting & CI** harness. |
| [csf-and-fragments.md](csf-and-fragments.md) | Power users | Confluence Storage Format (`.csf`) and how opaque fragments (drawio, images, users, links) are extracted and resolved. |
| [self-update.md](self-update.md) | Security-conscious users | The signed self-update trust model and how to disable it (`ATL_NO_UPDATE`). |
| [architecture.md](architecture.md) | Contributors | Hexagonal (ports & adapters) layout, the dependency rule, and extension points (new backend, new fragment type). |
| [RELEASING.md](RELEASING.md) | Maintainers | Signing key, release workflow, the Homebrew formula, and verification. |

New here? The [Quick start](../README.md#quick-start) gets you from install to a first result in four
commands; for non-interactive use jump to [usage.md → Scripting & CI](usage.md#scripting--ci).
