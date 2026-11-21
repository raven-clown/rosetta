# Changelog

All notable changes are listed here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- Each release attaches `rosetta` binaries for Linux, macOS and Windows on amd64 and arm64 with `checksums.txt`, and pushes a container image to `ghcr.io/raven-clown/rosetta` that runs `rosetta serve` on port 8080.
- The image keeps its database at `/data/rosetta.db`. Mount a volume there to keep it across restarts.
- SQLite and Postgres storage through `storage.dsn`, with migrations that run at startup and `rosetta migrate` to run them on their own.
- Session history is kept in the database, so it survives a restart and is shared by `rosetta serve`, `rosetta stdio` and every replica.
- A record of every run: agent, protocol, session, caller, status, error, times, tokens and request ID, kept for `runs.retention` (30 days by default).
- An admin API under `/admin/v1/`, described in `api/admin/openapi.yaml`: status, agents, API keys, runs, sessions and the audit log. Every resource has a `version` checked through `If-Match`, and every write adds an audit record.
- Agents and API keys made through the admin API are served at once, and reach other replicas within two seconds. `a2a` agents call another server and can forward the caller's token, so only an `admin` key can create, change or delete them.
- An `a2a` agent's `url` must be an `http://` or `https://` URL, in the config file too.
- On the first start, with no keys in the config or the database, rosetta makes an admin key and prints it once to stderr. `ROSETTA_BOOTSTRAP_ADMIN_KEY` sets it instead, and must be at least 32 characters.
- Reasoning tokens from transponder's `X-Usage-Reasoning-Tokens`, in run records and in `rosetta_tokens_total{direction="reasoning"}`.
- `rosetta_requests_total`, `rosetta_request_duration_seconds` and `rosetta_inflight_requests`, the request metrics shared by the mothership projects.
- `/readyz` reports the database.
- For library users: `store.Open` for the SQL store, `server.Options.Runs` to record runs, `server.Options.Registry` with `registry.Set` to change agents while serving, and `server.Options.Keys` to change API keys while serving.

### Changed

- **Breaking:** `rosetta serve` with no config now keeps its state in `rosetta.db` in the working directory and turns auth on with a first admin key, instead of serving the echo agent to anyone. Pass the printed key, or set `auth.disabled: true` in a config file to keep it open on this machine.
- **Breaking:** a config with auth on no longer needs `auth.api_keys`, since keys can come from the admin API.
- `ROSETTA_*` environment variables apply without a config file too.
- A 429 or 402 from transponder fails the run with its error code and `Retry-After`, such as `transponder: 429 Too Many Requests: rate_limited: slow down, retry after 7s`.
- With `auth.disabled`, the admin API answers only on this machine: it refuses requests that came through a proxy, and requests whose `Host` or `Origin` is not a local name, so a web page cannot reach it. Admin writes need `Content-Type: application/json`.

### Security

- rosetta no longer follows a redirect from the MCP server at `airlock.url` to another scheme, host or port, so that server can't send rosetta's headers and key somewhere else (GO-2026-6911). A request to a URL that redirects that way, even from `http` to `https`, now fails: set `airlock.url` to the address it ends up at.

## [0.1.0] - 2026-11-05

### Added

- `rosetta.Agent`, the one interface an agent implements, with `rosetta.Func` for plain functions.
- Protocol adapters for MCP (Streamable HTTP and stdio), A2A (JSON-RPC and REST), ACP (stdio) and AG-UI (SSE).
- `server` package: one `http.Handler` for every HTTP protocol, and `ServeStdio` for ACP and MCP.
- `rosetta serve` and `rosetta stdio` commands with a built-in echo agent.
- In-memory session history, scoped to the caller and capped in size.
- `kind: llm` agents: a tool-calling loop over any OpenAI-compatible endpoint, with tools from any MCP server such as airlock.
- `kind: a2a` agents, which serve a remote A2A agent over MCP, ACP and AG-UI.
- `--config rosetta.yaml` for `serve` and `stdio`, with `${VAR}` expansion and `ROSETTA_*` environment overrides.
- API key auth with `admin`, `member` and `viewer` roles. rosetta's own keys are never forwarded upstream.
- `/readyz`, which checks transponder and airlock, and Prometheus metrics on `/metrics`.
- The caller's `traceparent`, `tracestate`, `X-Request-ID` and `X-Project` are forwarded to transponder and airlock.

[Unreleased]: https://github.com/raven-clown/rosetta/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/raven-clown/rosetta/releases/tag/v0.1.0
