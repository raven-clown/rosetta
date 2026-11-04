# AGENTS.md

Instructions for coding agents working in this repository. Keep this file current: when a command, a package layout or a rule changes, update it in the same commit.

## Project

rosetta is a protocol bridge for agents, written in Go. You write an agent once against the `rosetta.Agent` interface, or declare one in YAML or through the admin API, and rosetta serves it over MCP, A2A, ACP (stdio) and AG-UI (SSE) at the same time. It calls tools through `airlock` and models through `transponder` when those are configured. Users are Go developers who want their agent reachable from editors, chat apps, web UIs and other agents, and platform teams who run config-only agents. The design is in `docs/design.md`.

It is one of four projects described in the shared contract at https://github.com/raven-clown/mothership/blob/main/CONTRACT.md. Follow the contract. A change to how the projects connect goes to the contract first.

## Commands

```shell
go build ./...
go test -race ./...
golangci-lint run ./...
make check   # lint plus tests with the 80% coverage check
```

`-race` needs cgo. On a machine without a C compiler, run `go test ./...` locally and let CI run the race detector.

## Layout

The spec in `docs/design.md` is approved. So far only the root package exists. The planned packages:

| Path | Holds |
|---|---|
| `/` (package `rosetta`) | `Agent`, `Card`, `Request`, `Message`, `Part`, events, `Stream`, `Func`. Types only, no protocol imports |
| `server/` | composes adapters into one `http.Handler`, stdio entry point |
| `protocol/` | adapter interfaces, the main place contributors add pieces |
| `protocol/mcp/`, `protocol/a2a/`, `protocol/acp/`, `protocol/agui/` | one adapter per protocol. Only these, `tools/mcp/` and `agents/a2a/` import a protocol SDK |
| `registry/` | live agent list from code, config and the database |
| `store/`, `store/migrations/` | store interface, in-memory and SQL stores, embedded migrations per dialect |
| `admin/`, `api/admin/openapi.yaml` | `/admin/v1` handlers and their OpenAPI description |
| `agents/llm/`, `agents/echo/`, `agents/a2a/` | built-in agent kinds, including a remote A2A agent |
| `llm/openai/` | OpenAI-compatible chat client used to reach `transponder` |
| `tools/mcp/` | tool source backed by an MCP server such as `airlock` |
| `config/` | YAML and `ROSETTA_*` environment overrides |
| `internal/run/`, `internal/httpx/` | runner (framing, history, run records) and HTTP middleware |
| `cmd/rosetta/` | `serve`, `stdio` and `migrate` commands |
| `docs/` | `design.md`, `protocols.md`, `issues.md`, `plans/` (implementation plans per milestone) |

Golden files under `testdata/` keep LF line endings (see `.gitattributes`).

## Rules

- Follow CONTRIBUTING.md: tests with every change, a failing test first for a bug fix, table-driven tests, no network in unit tests, no bare `time.Sleep`.
- Commit after every change, with a Conventional Commits message, and push it.
- Never mention AI, assistants or code generation in code, comments, docs, commit messages, pull requests or issues.
- Never add `Co-Authored-By`, `Generated with` or similar trailers.
- No em-dashes. Keep comments short and about why.
- Every feature ships in the free build. Never add a license check or a paid-only code path.
- Update CHANGELOG.md under Unreleased for anything users will notice, and update README.md when usage changes.
