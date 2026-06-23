# STANDARDS.md — Document Index

This file is an index of the repo's authoritative references. Read the relevant doc before
adding a feature; do not duplicate their content here.

## Core references

| Document | Use for |
|---|---|
| [`CLAUDE.md`](CLAUDE.md) | Project conventions, architecture rules, house rules, and everything an AI agent needs to know before touching code |
| [`docs/architecture.md`](docs/architecture.md) | Hexagonal layers, dependency rules, extension points (new backend, new fragment type) |
| [`docs/OUTPUT_CONTRACT.md`](docs/OUTPUT_CONTRACT.md) | CLI output formats (json/text/id), `emit`/`emitID` behaviour, sentinel→exit-code matrix |
| [`docs/csf-and-fragments.md`](docs/csf-and-fragments.md) | Confluence Storage Format internals, fragment types, the read-only parse contract |
| [`docs/self-update.md`](docs/self-update.md) | Self-update mechanism: manifest, ed25519 verification, anti-rollback, update base URL |
| [`docs/RELEASING.md`](docs/RELEASING.md) | Release checklist: VERSION bump, CHANGELOG, tagging, signing, publishing |
| [`docs/usage.md`](docs/usage.md) | End-user command reference and worked examples |

## Additional docs

| Document | Use for |
|---|---|
| [`docs/csf-markdown-testing.md`](docs/csf-markdown-testing.md) | Test strategy for the CSF→Markdown render pipeline |
| [`docs/README.md`](docs/README.md) | Docs directory overview |
| [`docs/proposals/`](docs/proposals/) | Design proposals and ADRs |
