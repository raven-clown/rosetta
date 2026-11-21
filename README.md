<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/assets/header-dark.svg">
  <img src="docs/assets/header-light.svg" alt="rosetta: agent protocol bridge. Write an agent once, serve it over MCP, A2A, ACP and AG-UI." width="100%">
</picture>

<div align="center">

[![ci](https://img.shields.io/github/actions/workflow/status/raven-clown/rosetta/ci.yml?branch=main&style=for-the-badge&label=ci&logo=githubactions&logoColor=white)](https://github.com/raven-clown/rosetta/actions/workflows/ci.yml)
[![release](https://img.shields.io/github/v/release/raven-clown/rosetta?style=for-the-badge&color=F97316)](https://github.com/raven-clown/rosetta/releases)
[![Go Reference](https://img.shields.io/badge/go-reference-F97316?style=for-the-badge&logo=go&logoColor=white)](https://pkg.go.dev/github.com/raven-clown/rosetta)
[![license](https://img.shields.io/badge/license-Apache--2.0-F97316?style=for-the-badge)](LICENSE)
[![openssf scorecard](https://img.shields.io/ossf-scorecard/github.com/raven-clown/rosetta?style=for-the-badge&label=openssf%20scorecard)](https://scorecard.dev/viewer/?uri=github.com/raven-clown/rosetta)

</div>

Write an agent once in Go. Serve it over MCP, A2A, ACP and AG-UI at the same time.

> **Status: early, [v0.2.0](https://github.com/raven-clown/rosetta/releases/tag/v0.2.0).** The four protocol adapters, the `llm`, `a2a` and `echo` agent kinds, a config file, SQLite and Postgres storage, run history and an admin API are done. Limits and usage events come next, and the Go API may still change before v1. Watch the repository or pick an issue if you want to help shape it.

## How it works

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/assets/how-it-works-dark.svg">
  <img src="docs/assets/how-it-works-light.svg" alt="Requests arrive over MCP, A2A, ACP and AG-UI. The same agent answers each one, and the reply goes back in the protocol it came from." width="100%">
</picture>

## Install

With Go 1.26 or later:

```bash
go install github.com/raven-clown/rosetta/cmd/rosetta@latest   # the rosetta command
go get github.com/raven-clown/rosetta@latest                  # the library, for agents written in Go
```

Each [release](https://github.com/raven-clown/rosetta/releases) has binaries for Linux, macOS and Windows, and an image that keeps its database in `/data`:

```bash
docker run -p 8080:8080 -v rosetta-data:/data ghcr.io/raven-clown/rosetta:v0.2.0
```

## Try it

Start it with no config at all:

```bash
go run github.com/raven-clown/rosetta/cmd/rosetta@latest serve
```

rosetta keeps its state in `rosetta.db` in the working directory and prints an admin key to stderr on the first start. It is shown once, so copy it:

```
rosetta: created the first admin key. It is shown only this once:

    rsk_...
```

Keep it in a variable for the commands below:

```bash
export KEY='rsk_...'
```

Then talk to the built-in echo agent:

```bash
curl -N -X POST localhost:8080/agui/echo -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"threadId":"t1","runId":"r1","messages":[{"id":"u1","role":"user","content":"hello"}],"tools":[],"context":[],"state":null}'
```

The same agent answers MCP on `localhost:8080/mcp`, has an A2A card at `localhost:8080/.well-known/agent-card.json`, and talks ACP to an editor with `rosetta stdio --protocol acp`. Set `ROSETTA_BOOTSTRAP_ADMIN_KEY` to choose the first key yourself, for Docker or CI. It must be at least 32 characters, such as the output of `openssl rand -base64 32`.

Add an agent without writing a file:

```bash
curl -X POST localhost:8080/admin/v1/agents -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"name":"greeter","kind":"echo","description":"Says it back."}'
```

An `llm` agent needs a model endpoint first: set `transponder.url` in a config file, or `ROSETTA_TRANSPONDER__URL=http://localhost:4000/v1`, then create it with `"kind":"llm","model":"claude-sonnet-5-5"`.

For agents kept in git, write a config like [examples/rosetta.yaml](examples/rosetta.yaml) and start it with:

```bash
ROSETTA_LOCAL_KEY=dev-key rosetta serve --config examples/rosetta.yaml
```

Callers then send `Authorization: Bearer dev-key`. `/readyz` checks that the database, transponder and airlock answer, and `/metrics` serves Prometheus metrics.

## Storage and the admin API

State lives in SQL: session history, a record of every run, and the agents and API keys made through the admin API. SQLite is the default. For a team, or several replicas, point every instance at one Postgres database:

```yaml
storage:
  dsn: postgres://rosetta:secret@db:5432/rosetta?sslmode=verify-full
```

Say `sslmode` in the DSN. Without it, pgx uses `prefer`, which falls back to plaintext and does not check the server's certificate. Use `verify-full` across a network and `disable` only on a private one.

Migrations run at startup, and `rosetta migrate --config rosetta.yaml` runs them on their own, for a separate deploy step. A change made through the admin API reaches every replica within two seconds.

The admin API lives under `/admin/v1/` and is described in [api/admin/openapi.yaml](api/admin/openapi.yaml):

| Endpoint | What |
|---|---|
| `GET /status` | version, uptime, storage, protocols and upstreams |
| `GET, POST /agents`, `GET, PATCH, DELETE /agents/{name}` | agents from code, config and the API. Config and code agents are read-only |
| `GET, POST /api-keys`, `DELETE /api-keys/{id}` | API keys. A new key is shown once, and only an `admin` key can manage them |
| `GET /runs`, `GET /runs/{id}` | run history by agent, protocol, status and time |
| `GET /sessions`, `GET, DELETE /sessions/{id}` | conversations and their messages |
| `GET /audit` | every write made through the API, and who made it |

Every resource has a `version`. Send it in `If-Match` on `PATCH` and `DELETE`, and a stale one gets 409 `version_conflict`, so two people editing at once never overwrite each other.

## Why

Each agent protocol reaches a different set of clients:

| Protocol | Who talks it |
|---|---|
| [MCP](https://modelcontextprotocol.io) | Claude Desktop, Cursor and other MCP clients, plus other agents |
| [A2A](https://a2a-protocol.org) | other agents and orchestrators |
| [ACP](https://agentclientprotocol.com) | editors such as Zed and JetBrains IDEs |
| [AG-UI](https://docs.ag-ui.com) | web front ends such as CopilotKit |

Supporting all four today means four servers, four event models and four ways of handling sessions, cancellation and auth. rosetta gives you one small interface and does the translation:

```go
agent := rosetta.Func(rosetta.Card{Name: "echo"},
	func(ctx context.Context, req *rosetta.Request, out rosetta.Stream) error {
		return out.Text("you said: " + req.Input[0].Text)
	})

srv := server.New(server.Options{}, agent)
http.ListenAndServe(":8080", srv.Handler())
```

That one value answers on `/mcp`, `/a2a/echo` and `/agui/echo`, and `rosetta stdio --protocol acp` serves it to an editor.

You don't need to write Go at all. Declare an agent in YAML or through the admin API:

```yaml
agents:
  - name: helper
    kind: llm
    model: claude-sonnet-5-5
    system_prompt: You are a helpful assistant.
    tools: ["github__*"]
```

Or wrap an existing A2A agent, so it shows up in Zed, Claude Desktop and your web app without any code:

```yaml
agents:
  - name: travel
    kind: a2a
    url: https://agents.example.com/travel
```

## How it compares

**Agent frameworks** such as [adk-go](https://github.com/google/adk-go), [eino](https://github.com/cloudwego/eino) and [trpc-agent-go](https://github.com/trpc-group/trpc-agent-go) own the agent loop: models, tools, workflows, memory. rosetta doesn't. It owns the edge, so your agent can be a hand-written loop, a framework agent or a remote service. The two fit together: a small bridge turns a framework agent into a `rosetta.Agent`.

**Hosted agent platforms** serve some of these protocols too, but keep parts of it behind a paid plan. rosetta has **no paid edition and no license key**. Roles, audit logs, SSO, Postgres clustering and the admin API all ship in the Apache-2.0 build. See the comparison, with links to each vendor's pricing page, in [the design](docs/design.md#everything-is-free).

## What v0.2.0 has

- One Go interface, `rosetta.Agent`, and four protocol adapters (MCP over Streamable HTTP and stdio, A2A over JSON-RPC and REST, ACP over stdio, AG-UI over SSE)
- Built-in agent kinds: `llm` (a tool-calling loop over any OpenAI-compatible API), `a2a` (a remote A2A agent) and `echo`
- Tools through any MCP server, models through any OpenAI-compatible endpoint, with input, output and reasoning tokens read from transponder
- A YAML config with `${VAR}` expansion and `ROSETTA_*` overrides, or no file at all
- SQLite and Postgres storage, a record of every run, `rosetta migrate`
- An admin API for agents, API keys, runs, sessions and the audit log, with a first admin key on the first start
- API keys with `admin`, `member` and `viewer` roles, `/healthz`, `/readyz`, Prometheus `/metrics` with the request metrics every mothership project shares, JSON logs

## Roadmap

| Release | What |
|---|---|
| v0.1.0 | Core types, runner, the four adapters, `llm`, `a2a` and `echo` agents, config file, API keys, health and metrics |
| v0.2.0 | SQLite and Postgres storage, run history, admin API, first-run admin key, audit log |
| v0.3.0 | Limits from shared profiles, usage events, settings and usage in the admin API, A2A tasks in SQL |
| later | OIDC sign-in, OpenTelemetry traces, input-required flows, files and images, framework bridges |

## Part of mothership

rosetta is one of four projects that work alone and fit together, following a [shared contract](https://github.com/raven-clown/mothership/blob/main/CONTRACT.md):

- [airlock](https://github.com/raven-clown/airlock), an MCP gateway. rosetta agents call tools through it.
- [transponder](https://github.com/raven-clown/transponder), an LLM gateway. rosetta agents call models through it.
- [logbook](https://github.com/raven-clown/logbook), shared agent memory served over MCP.

None of them is required. Each link is a URL in the config.

## Help build it

The easiest way in is a protocol adapter: each protocol lives in its own package under `protocol/`, with golden tests that show exactly what goes over the wire. Issues labelled [`good first issue`](https://github.com/raven-clown/rosetta/labels/good%20first%20issue) say which files to change and which tests to add. Read [CONTRIBUTING.md](CONTRIBUTING.md) first.

- Found a bug, or want something new? Open an issue with one of the [issue forms](https://github.com/raven-clown/rosetta/issues/new/choose). Each one says what to include.
- Pull requests come with a template that lists what reviewers check first in this project, so you know what to cover before you open one.

## Contributors

Thanks to everyone who has helped build rosetta.

<a href="https://github.com/raven-clown/rosetta/graphs/contributors"><img src="https://contrib.rocks/image?repo=raven-clown/rosetta" alt="Contributors to rosetta"></a>

## License

[Apache-2.0](LICENSE)
