# Changelog

All notable changes are listed here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- `rosetta.Agent`, the one interface an agent implements, with `rosetta.Func` for plain functions.
- Protocol adapters for MCP (Streamable HTTP and stdio), A2A (JSON-RPC and REST), ACP (stdio) and AG-UI (SSE).
- `server` package: one `http.Handler` for every HTTP protocol, and `ServeStdio` for ACP and MCP.
- `rosetta serve` and `rosetta stdio` commands with a built-in echo agent.
- In-memory session history, scoped to the caller and capped in size.
