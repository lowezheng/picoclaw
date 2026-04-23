# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

PicoClaw is an ultra-lightweight personal AI assistant written in Go. It supports 30+ LLM providers, 18+ chat channels (Telegram, Discord, WhatsApp, WeChat, etc.), MCP servers, skills, cron jobs, and a Web UI launcher. The core binary targets <10MB RAM and runs on $10 hardware (ARM, MIPS, RISC-V, LoongArch, x86).

## Tech Stack

- **Backend**: Go 1.25+, Cobra CLI, zerolog, SQLite (modernc.org/sqlite)
- **Web Launcher**: React 19 + Vite + Tailwind CSS v4 + TanStack Router/Query + shadcn/ui + Jotai
- **Build tools**: Make, golangci-lint, goreleaser
- **Package manager**: pnpm 10.33.0+ for frontend

## Common Commands

### Build

```bash
# Core binary for current platform (runs go generate first)
make build

# macOS: if you hit C dependency issues, force CGO-free build explicitly
CGO_ENABLED=0 go build -tags goolm,stdjson -o build/picoclaw ./cmd/picoclaw

# Web UI launcher (requires frontend build)
make build-launcher

# All Makefile-managed platforms
make build-all

# Raspberry Pi Zero 2 W (32-bit + 64-bit)
make build-pi-zero

# With WhatsApp native support (larger binary)
make build-whatsapp-native

# TUI launcher (terminal UI)
make build-launcher-tui
```

Build artifacts go to `build/`. Default build tags are `goolm,stdjson`. The `generate` target runs `go generate ./...` before every build.

### Test

```bash
# All tests (core + web backend)
make test

# Single test
GOFLAGS="-tags goolm,stdjson" go test -run TestName -v ./pkg/agent/

# Core tests only (excludes web/)
CGO_ENABLED=0 go test -tags goolm,stdjson ./... | grep -v web/

# Web backend tests
cd web/backend && go test ./...
```

### Lint / Format

```bash
make lint        # golangci-lint + docs lint
make fmt         # Auto-format via golangci-lint
make fix         # Auto-fix lint issues
make vet         # go vet
make lint-docs   # Docs naming/layout consistency
make check       # Full pre-commit: deps + fmt + vet + test + lint-docs
```

### Web Launcher Development

```bash
# Install frontend deps
cd web/frontend && pnpm install --frozen-lockfile

# Dev mode (runs both frontend Vite dev server and backend Go server)
cd web && make dev

# Build frontend for embedding into Go binary
cd web && make build-frontend

# Build launcher binary
cd web && make build
```

The web launcher backend (`web/backend/main.go`) embeds the compiled frontend from `web/backend/dist/` and serves it on `http://localhost:18800` by default.

### Run

```bash
# Initialize config and workspace
./build/picoclaw onboard

# Interactive agent chat
./build/picoclaw agent

# Start gateway server
./build/picoclaw gateway

# Start web launcher
./build/picoclaw-launcher
```

Configuration lives at `~/.picoclaw/config.json` (insensitive data) and `~/.picoclaw/.security.yml` (sensitive credentials). See `config/config.example.json` for the full schema.

## High-Level Architecture

### Entry Point

`cmd/picoclaw/main.go` bootstraps the Cobra CLI. Subcommands are defined in `cmd/picoclaw/internal/`. The launcher has separate entry points: `web/backend/main.go` (Web UI) and `cmd/picoclaw-launcher-tui/` (TUI).

### Core Packages

| Package | Responsibility |
|---------|--------------|
| `pkg/agent/` | **Agent loop** — the heart of PicoClaw. `loop.go` orchestrates the turn-based LLM conversation loop. `turn.go` handles a single turn. `subturn.go` spawns isolated nested agent loops. `steering.go` injects messages into running loops. `hooks.go` / `hook_process.go` implement the hook system. `context.go` manages conversation context and budget. `eventbus.go` broadcasts internal events. |
| `pkg/channels/` | **Chat platform integrations** — 18+ channels (telegram, discord, whatsapp, weixin, qq, slack, matrix, feishu, dingtalk, line, wecom, irc, onebot, vk, maixcam, pico). Each is a subpackage implementing a common interface. `manager.go` orchestrates channel lifecycle. `openresponses/` is the OpenAI-compatible responses API. |
| `pkg/providers/` | **LLM provider integrations** — 30+ providers via protocol-specific subpackages (openai_compat, anthropic, azure, bedrock, etc.). `factory_provider.go` is the central provider factory. `httpapi/` contains shared HTTP client logic. `cli/` is the CLI provider facade. `cooldown.go` handles rate-limit backoff. `error_classifier.go` classifies transient vs permanent errors. |
| `pkg/tools/` | **Built-in tools** — shell execution, file I/O, web search, cron, spawn/subagent, skills installation, hardware I2C/SPI, MCP client. `registry.go` is the tool registry. `shell.go` is the most complex tool (PTY support, timeout, sandbox). `spawn.go` / `subagent.go` delegate to the agent loop. `integration/` contains per-tool subpackages (fs, hardware, etc.). |
| `pkg/routing/` | **Runtime routing** — `route.go` dispatches inbound messages to the right agent and session. `router.go` / `classifier.go` choose between primary and light models based on message complexity. |
| `pkg/gateway/` | **HTTP gateway** — shared HTTP server for webhook-based channels and the OpenAI-compatible API. `gateway.go` sets up routes; `listen.go` handles server lifecycle. |
| `pkg/memory/` | **Memory persistence** — JSONL-based conversation storage with summarization. |
| `pkg/session/` | **Session management** — scope allocation, alias compatibility, metadata. |
| `pkg/bus/` | **Event bus** — internal pub/sub for decoupled component communication. |
| `pkg/config/` | **Configuration** — JSON config parsing, version migration, `.security.yml` handling. |
| `pkg/skills/` | **Skills system** — loads `SKILL.md` files from workspace, ClawHub registry integration. |
| `pkg/mcp/` | **MCP client** — native Model Context Protocol support (stdio, SSE, HTTP transports). |
| `pkg/cron/` | **Scheduled tasks** — cron expression parsing and job execution. |

### Key Runtime Flow

```
InboundMessage (channel)
  -> pkg/channels/ manager normalizes -> bus.InboundContext
  -> pkg/routing/ RouteResolver resolves agent + session policy
  -> pkg/session/ allocates session scope
  -> pkg/agent/ loop_turn.go runs one turn
       -> Router.SelectModel() picks primary or light model
       -> Provider factory creates client
       -> LLM call
       -> Tool calls loop through pkg/tools/registry.go
       -> SubTurns spawn nested agent loops if needed
       -> Steering injects messages between tool calls
       -> Hooks fire at before_llm / after_llm / before_tool / after_tool
       -> EventBus broadcasts events
  -> Response routed back through channel
```

### Build Tags

- `goolm` — Required for Ollama provider (enables CGO-free OLM support)
- `stdjson` — Use standard library `encoding/json` instead of third-party
- `whatsapp_native` — Include native WhatsApp client (whatsmeow), increases binary size
- `bedrock` — Include AWS Bedrock provider

Default: `GO_BUILD_TAGS=goolm,stdjson`. On MIPS builds, `goolm` is excluded because it lacks support.

### Code Patterns

- **Provider pattern**: New providers implement the interface in `pkg/providers/common/` and register in `pkg/providers/factory_provider.go`.
- **Channel pattern**: New channels implement `Channel` interface in `pkg/channels/base.go` and register in `pkg/channels/registry.go`.
- **Tool pattern**: New tools implement `Tool` interface, register in `pkg/tools/registry.go`, and usually have a facade in `pkg/tools/integration/`.
- **Hook system**: Supports in-process and out-of-process (JSON-RPC over stdio) hooks. Hook types: Observer, LLMInterceptor, ToolInterceptor, ToolApprover. See `docs/architecture/hooks/README.md`.
- **SubTurn**: Isolated nested agent loops with ephemeral sessions, depth limit 3, concurrency limit 5. See `docs/architecture/subturn.md`.
- **Steering**: Injects messages into a running agent loop between tool calls. See `docs/architecture/steering.md`.

### Testing Notes

- Core tests exclude `web/` via `grep -v`. Web tests are run separately.
- Many agent tests use mock providers; see `pkg/agent/mock_provider_test.go`.
- Build tags must be passed to `go test`: `-tags goolm,stdjson`.
- The project uses `testify` for assertions.

### Important Files

- `Makefile` — All build/test/lint targets
- `config/config.example.json` — Full configuration schema
- `docs/architecture/` — Deep dives into agent loop, routing, session, hooks, steering, subturns
- `CONTRIBUTING.md` — Branch off `main`, squash merge, AI disclosure required in PRs
- `.golangci.yaml` — Linter config (many rules disabled due to rapid development)
