# Sumika

English | [简体中文](README.zh-CN.md)

[![CI](https://github.com/tsugumi42/sumika-public/actions/workflows/ci.yml/badge.svg)](https://github.com/tsugumi42/sumika-public/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
![Status: early development](https://img.shields.io/badge/status-early%20development-orange.svg)

Sumika is a local, always-on home for AI characters. Several characters live on one computer, each with their own persona, model and long-term memory. You chat with them in a native desktop window, ask them to help with tasks on your machine, and they can take part in QQ private and group chats while remembering who is who.

> [!WARNING]
> **Sumika is in early development.** There are no releases or installers yet, configuration keys and on-disk formats still change without migration notes, and some features described in the docs are only partly implemented. It is not ready for everyday use by people who are not working on it.
>
> Most design documents, specs and code comments are written in Chinese. Issues and pull requests in English are welcome.

## Features

What works today (Windows):

- **Native desktop app.** One executable runs both the backend and a GPU-drawn UI built with [mygo](https://github.com/egoist/mygo) and [CrychicUI](https://github.com/ELY-Open-Source/CrychicUI). It sits in the system tray and does not open a web server or a port.
- **Local private and group chats.** Streaming replies, stop, quoting, and group conversations where several characters take turns.
- **Tool permissions.** Shell commands and file changes go through a permission policy. Sumika shows an approval card when the policy requires confirmation. Checkpoints can restore supported file changes.
- **Long-term memory.** Per-character memory plus a shared pool for facts about the owner, with full-text and optional local-embedding search. Background consolidation is opt-in.
- **QQ integration.** Connects to a OneBot-compatible endpoint, stores every message in a local archive, and decides when a character should speak. Sending is off by default and gated by an outbound guard.
- **Link reading.** Understands links from X, Bilibili, YouTube, pixiv, Steam, BOOTH and others that appear in chats.

Planned: a TypeScript plugin layer that reloads in-process, an MCP tier, settings editing in the UI, and packaged releases. See the [1.0 roadmap](docs/路线图-1.0.md) (Chinese).

## How it fits together

| Part | What it does |
|---|---|
| **Characters** | Persona card, voice, model choice and private memory, stored under `data/characters/<id>/` (never committed). |
| **Channels** | Where messages come from: local private chat, local group chat, QQ private chat, QQ group. Every memory records its source. |
| **Kernel** | Runs turns, tools and approvals; plugins (memory, link reader, and more) hook into it through a registry. |
| **Model gateway** | Model calls go to a local [Magpie](https://github.com/yetone/magpie) gateway (`127.0.0.1:3425`) by default; each character picks a `provider/model`. |
| **Desktop UI** | Lives in the same process as the backend and talks to it directly. |

The architecture overview is in [docs/架构.md](docs/架构.md).

## Getting started (development)

Requirements:

- Windows 10 or 11
- [Go](https://go.dev/dl/) 1.27 or newer
- [Bun](https://bun.sh/) 1.4 or newer, for the repository scripts and git hooks
- A running [Magpie](https://github.com/yetone/magpie) gateway, or another OpenAI-compatible endpoint configured in `magpie.baseUrl`

```bash
git clone https://github.com/tsugumi42/sumika-public.git sumika
cd sumika
bun install                # repository scripts and git hooks
mkdir -p data/config
cp sumika.example.yml data/config/sumika.yml   # every key is documented inline
```

Build and run the desktop app:

```bash
cd native
go build -trimpath -ldflags "-s -w -H windowsgui" -o sumika.exe .
./sumika.exe -root ..        # or: ./sumika.exe -demo  (sample data, touches nothing)
```

The app starts in read-only `archive` mode. Use the tray menu to start receiving and to enable local chat. A headless entry point for diagnostics is `go run ./cmd/sumika-go -mode archive`.

Fonts are not included in the repository. The UI falls back to system fonts; see [native/fonts](native/fonts) for the licenses of the fonts the project is designed for.

All command-line tools and flags are listed in [docs/subsystems/go-tools.md](docs/subsystems/go-tools.md).

## Running tests

```bash
cd native
go vet ./...
go test -short ./...
```

CI runs on Windows and Linux for pushes and pull requests. Changes go through a [GitHub pull request](docs/规格-合并走PR.md). See [CONTRIBUTING.md](CONTRIBUTING.md) for local checks.

## Repository layout

```
native/        Go backend and desktop UI (the active code base)
  cmd/         command-line entry points (sumika-go, sumika-ctl, evaluators, ...)
  internal/    kernel, channels, memory, policy, UI components, ...
scripts/       Bun/TypeScript scripts: merge queue, checks, git hooks
docs/          specs, decision records (ADRs), roadmap, notes
prompts/       prompt templates
characters/    one example character card
testdata/      shared test fixtures (synthetic data only)
```

## Documentation

- [CONTRIBUTING.md](CONTRIBUTING.md): environment setup, testing, commit conventions, code map
- [docs/README.md](docs/README.md): index of all documents
- [docs/决策记录.md](docs/决策记录.md): architecture decision records
- [AGENTS.md](AGENTS.md): project conventions for contributors and coding agents
- [docs/使用说明.md](docs/使用说明.md): user guide

These documents are in Chinese.

## Contributing

Sumika is developed mainly by one person with AI coding agents, so the process is a little unusual. Bug reports and small pull requests are welcome. Please open an issue first for anything larger. Read [CONTRIBUTING.md](CONTRIBUTING.md) before sending code.

## Acknowledgements

- [mygo](https://github.com/egoist/mygo) and [CrychicUI](https://github.com/ELY-Open-Source/CrychicUI) for the native UI toolkit
- [Magpie](https://github.com/yetone/magpie) for the local model gateway

Third-party licenses are listed in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).

## License

[MIT](LICENSE)
