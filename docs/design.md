# rosetta design

Status: draft for review
Module: `github.com/raven-clown/rosetta`
Contract: follows `mothership/CONTRACT.md`

## 1. What rosetta is

Write an agent once, against one small Go interface. rosetta serves it over four agent protocols at the same time:

| Protocol | Who talks it | Transport in rosetta |
|---|---|---|
| MCP | Claude Desktop, Cursor, any MCP client, other agents | Streamable HTTP, and stdio for local use |
| A2A | other agents and orchestrators | HTTP, JSON-RPC and REST bindings |
| ACP | editors such as Zed and JetBrains | stdio |
| AG-UI | web front ends (CopilotKit and others) | HTTP POST answered with SSE |

When configured, the agent reaches tools through `airlock` (an MCP server) and models through `transponder` (an OpenAI-compatible API). Both links are plain URLs. Any MCP server and any OpenAI-compatible endpoint work in their place, so rosetta runs fine without the rest of mothership.

rosetta is two things in one repository:

1. A Go library. You implement `rosetta.Agent`, hand it to `server.New`, and get an `http.Handler` plus a stdio entry point.
2. A binary. You declare agents (a model, a system prompt, a tool allowlist) in YAML or through the admin API, and run `rosetta serve`. No Go needed. State lives in embedded SQLite by default, or Postgres for teams.

Every feature ships in the Apache-2.0 build. There is no paid edition, and the README says so.

### Who it is for

- Go developers who already have agent logic (their own loop, or one built with a framework) and want it reachable from editors, chat apps, web UIs and other agents without writing four servers.
- Platform teams that want a config-only agent which uses tools behind `airlock` and models behind `transponder`, managed later from the shared web console.

### What success looks like for the first release

- A five-minute demo: one YAML file, one binary, and the same agent answers in the MCP Inspector, in Zed over ACP, from an A2A client, and in an AG-UI web page.
- The Go interface fits on one screen and does not change when a protocol changes.
- A contributor can add or update a protocol by touching one package.

## 2. Where rosetta differs from agent frameworks

`google/adk-go`, `cloudwego/eino` and `trpc-agent-go` are agent frameworks. They own the agent loop: model abstractions, tool calling, graphs or workflows, sessions, memory, evaluation. Some of them also ship a server for one or two protocols, as an add-on to agents built with that framework.

rosetta does not own the loop. It owns the edge:

- It is the serving layer, not the thinking layer. Your agent can be a hand-written loop, an adk-go agent, an eino graph, or a call to a remote service. rosetta only needs `Run`.
- All four protocols are first class and served together from one agent value, with the same session, identity and tracing behaviour on each.
- Its event model is a small common subset designed for translation. Frameworks have rich internal event types tied to their own runtime.
- Tools and models are outside the process by default (`airlock`, `transponder`), so policy, audit and cost live in gateways rather than inside each agent.

The two fit together. A thin bridge package (for example `contrib/adk` or `contrib/eino`, each its own Go module) can turn a framework agent into a `rosetta.Agent`. Those bridges are not in the first release, and they are a good place for outside contributions.

## 3. State of the protocols and their Go SDKs

Checked on 2026-11-04 against the module proxy.

| Protocol | Spec version | Go SDK | Version | Notes |
|---|---|---|---|---|
| MCP | 2026-07-28 (also 2025-11-25 and older) | `mark3labs/mcp-go` | v1.5.0 | server and client, Streamable HTTP and stdio, tasks, elicitation, progress |
| MCP | same | `modelcontextprotocol/go-sdk` | v1.8.0 | official SDK, same spec coverage |
| A2A | 1.0 | `a2aproject/a2a-go/v2` | v2.7.0 | official. `a2asrv.AgentExecutor` returns `iter.Seq2[a2a.Event, error]`. JSON-RPC, REST and gRPC handlers. `a2asrv` alone does not pull in gRPC |
| ACP | protocol version 1 | `coder/acp-go-sdk` | v0.13.5 | generated from the ACP schema, no dependencies, `AgentSideConnection` over any reader and writer |
| AG-UI | current event set (text, tool calls, state, reasoning, activity, steps) | `ag-ui-protocol/ag-ui/sdks/community/go` | untagged, pseudo-versions only | event types, `RunAgentInput`, SSE writer. No server. Pulls in logrus through its decoder |

## 4. Wrap the SDKs or speak the wire?

Decision: wrap the SDKs, inside one adapter package per protocol, behind rosetta's own types.

Reasons:

- Each protocol is still moving. MCP shipped a new spec version this year that changed headers and removed stream resumption. A2A 1.0 changed task state names and content types. The SDK teams track those changes full time. A small project that reimplements four wire formats falls behind within months.
- Session handling, JSON-RPC framing, cancellation and capability negotiation are easy to get subtly wrong. The SDKs carry conformance tests for exactly that.
- The cost of wrapping is the dependency graph and SDK API churn. Both stay contained, because only `protocol/<name>` imports an SDK. Nothing outside that package sees an SDK type.

Per protocol:

- **MCP: `mark3labs/mcp-go`.** Both SDKs are complete. mcp-go wins because it is the one the maintainer knows inside out, it has the larger user base, and rosetta also needs an MCP client for `airlock` (one SDK for both directions). Switching to the official SDK later is a change inside `protocol/mcp` and `tools/mcp` only.
- **A2A: `a2aproject/a2a-go/v2`.** Official, on spec 1.0, and its executor already streams with iterators.
- **ACP: `coder/acp-go-sdk`.** Generated from the schema, zero dependencies, used by real editors.
- **AG-UI: the community SDK's `core/types`, `core/events` and `encoding/sse` packages, pinned to one pseudo-version.** rosetta writes its own small HTTP handler around them. If that SDK stalls or its dependencies grow, AG-UI events are flat JSON objects, and replacing the imported types with local structs is a one-package change. The golden tests in section 13 make that swap safe.

### How rosetta keeps up as protocols change

1. **One package per protocol.** Protocol churn lands in `protocol/<name>` and nowhere else. The core interface in section 5 is versioned with rosetta, not with any protocol.
2. **Golden wire tests.** Every adapter runs a fixed scripted agent and records the exact bytes it sends (JSON-RPC frames, SSE frames, ACP notifications) into `testdata/*.golden`. An SDK bump that changes the wire shows up as a readable diff in the pull request.
3. **Round-trip tests with the SDK's own client.** The MCP client from mcp-go, `a2aclient`, the ACP `ClientSideConnection`, and the AG-UI SSE client each talk to rosetta in-process. If the SDK's client and server disagree with rosetta's mapping, the test fails.
4. **Automated dependency bumps.** Renovate opens a pull request for each SDK release. CI runs the golden and round-trip suites on it.
5. **A declared support table.** `docs/protocols.md` lists the spec version each adapter targets and which rosetta events it maps or drops. Each adapter's tests check that the table matches what the adapter declares.
6. **An escape hatch.** `rosetta.Extension` carries a protocol-specific payload that only the named adapter passes through. Agents can use a new protocol feature on day one, before rosetta models it in the common event set.
7. **Conformance suites later.** Nightly CI jobs against the MCP conformance suite and the A2A TCK once the core is stable (not in the first release).

## 5. The agent interface

Root package `rosetta`. Types only, no protocol imports.

```go
type Agent interface {
	Card() Card
	Run(ctx context.Context, req *Request, out Stream) error
}

type Card struct {
	Name        string
	Description string
	Version     string
	Skills      []Skill
}

type Skill struct {
	ID          string
	Name        string
	Description string
	Tags        []string
	Examples    []string
}

type Request struct {
	SessionID string
	RunID     string
	Input     []Part
	History   []Message
	State     json.RawMessage
	Caller    Caller
}

type Caller struct {
	Token     string
	RequestID string
	Trace     http.Header
}

type Message struct {
	Role  Role
	Parts []Part
}

type Part struct {
	Text string
	Data json.RawMessage
}

type Stream interface {
	Send(Event) error
	Text(s string) error
	Reasoning(s string) error
	ToolCall(id, name string, args json.RawMessage) error
	ToolResult(id string, result json.RawMessage, isError bool) error
}
```

`Func` lets a plain function be an agent:

```go
agent := rosetta.Func(rosetta.Card{Name: "echo"}, func(ctx context.Context, req *rosetta.Request, out rosetta.Stream) error {
	return out.Text("you said: " + req.Input[0].Text)
})
```

Design choices:

- **Push stream, not a returned iterator.** Agent code just calls `out.Text(...)` from wherever it is, including goroutines. `Send` returns an error once the client has gone, so long loops can stop early. Adapters that want an iterator (A2A) convert on their side.
- **The run ends when `Run` returns.** `nil` means completed. An error means failed. A cancelled `ctx` means cancelled. There is no separate "done" event to forget.
- **`Input` and `History` are separate.** Protocols disagree on history. AG-UI sends the full conversation every time. A2A, ACP and MCP send only the new turn. rosetta fills `History` from its session store when the protocol does not send it, so the agent always sees the same shape.
- **`Part` holds text or JSON data in v0.1.** Files and images come later (section 14).

### Events

```go
type TextDelta      struct{ MessageID, Text string }
type ReasoningDelta struct{ Text string }
type ToolCall       struct{ ID, Name string; Args json.RawMessage }
type ToolResult     struct{ ID string; Result json.RawMessage; IsError bool }
type Step           struct{ Name string; Finished bool }
type StateSnapshot  struct{ State json.RawMessage }
type Artifact       struct{ Name string; Parts []Part }
type Usage          struct{ InputTokens, OutputTokens int64; CostUSD float64 }
type Extension      struct{ Protocol string; Value any }
```

`Event` is a sealed interface over these types. `ToolCall` and `ToolResult` report tools the agent itself calls (for example through `airlock`), so clients can show them. `Usage` is not sent to clients in v0.1. The runner adds it up on the run record, which feeds the admin API's usage view.

The runner frames the stream before any adapter sees it: it assigns message IDs, and it closes the open text message when a non-text event arrives. Adapters therefore never reimplement "when does a message start and end", which AG-UI and ACP both need.

## 6. Protocol mapping

| rosetta | MCP | A2A | ACP | AG-UI |
|---|---|---|---|---|
| agent | one tool per agent, named after it | one agent card per agent | one ACP agent per process | one endpoint per agent |
| `SessionID` | optional `session_id` tool argument | `contextId` | `sessionId` from `session/new` | `threadId` |
| `RunID` | generated | `taskId` | generated per `session/prompt` | `runId` |
| run starts | `tools/call` | `SendMessage` or `SendStreamingMessage` | `session/prompt` | POST `RunAgentInput` |
| `TextDelta` | `notifications/progress` message when the client sent a progress token. Full text in the final result | artifact update on artifact `response`, `append: true` | `agent_message_chunk` | `TEXT_MESSAGE_START`, `_CONTENT`, `_END` |
| `ReasoningDelta` | dropped | dropped | `agent_thought_chunk` | `REASONING_START`, `REASONING_MESSAGE_*`, `REASONING_END` |
| `ToolCall` | progress message `calling <name>` | status update `working` with a data part | `tool_call` | `TOOL_CALL_START`, `_ARGS`, `_END` |
| `ToolResult` | dropped | status update `working` with a data part | `tool_call_update` (completed or failed) | `TOOL_CALL_RESULT` |
| `Step` | progress message | status update `working` with the step name | dropped in v0.1 | `STEP_STARTED`, `STEP_FINISHED` |
| `StateSnapshot` | dropped | dropped | dropped | `STATE_SNAPSHOT` |
| `Artifact` | `structuredContent` (data) and extra content blocks | artifact update | message chunk | `CUSTOM` event `artifact` |
| `Run` returns nil | tool result | status `TASK_STATE_COMPLETED`, last artifact chunk | stop reason `end_turn` | `RUN_FINISHED` |
| `Run` returns error | tool result with `isError: true` | status `TASK_STATE_FAILED` with message | JSON-RPC error | `RUN_ERROR` |
| cancellation | `notifications/cancelled` | `CancelTask` | `session/cancel` | client closes the stream |
| identity | bearer token on the HTTP request, none on stdio | bearer token | none (stdio), falls back to config | bearer token |

A2A runs start with a `TASK_STATE_SUBMITTED` then `TASK_STATE_WORKING` status before the first agent event, so non-streaming `SendMessage` callers get a proper task back.


## 7. Serving topology

### HTTP: `rosetta serve`

One listener (`listen_addr`, default `:8080`) serves:

| Path | What |
|---|---|
| `/mcp` | MCP Streamable HTTP. Lists every agent as a tool |
| `/a2a/{agent}` | A2A JSON-RPC (POST) and REST (`/a2a/{agent}/v1/...`) |
| `/a2a/{agent}/.well-known/agent-card.json` | agent card |
| `/.well-known/agent-card.json` | card of the default agent (the first one, or `default_agent`) |
| `/agui/{agent}` | AG-UI: POST `RunAgentInput`, SSE response |
| `/admin/v1/...` | admin API for the future console (section 11) |
| `/healthz`, `/readyz`, `/metrics` | operations, per the contract |

Each protocol can be switched off or moved to another path in config.

Agents can be added, changed and removed at runtime through the admin API, so adapters look an agent up by name on each request instead of binding a fixed list at startup. When the agent list changes, the MCP adapter sends `notifications/tools/list_changed`.

Because the MCP endpoint is an ordinary Streamable HTTP server, `airlock` can list rosetta as one of its downstream servers. Every rosetta agent then shows up behind `airlock` as `rosetta__<agent>`, with airlock's login, permissions and audit in front.

### stdio: `rosetta stdio`

Only one protocol can own stdin and stdout, so stdio is a separate mode:

```
rosetta stdio --protocol acp --agent helper
rosetta stdio --protocol mcp
```

Editors launch this command as a child process. It reads the same config and the same database as `rosetta serve`, so agents created in the console are usable from an editor. In stdio mode logs go to stderr, since stdout carries the protocol. The contract says logs are JSON on stdout, so this needs a one-line note in the contract (see section 16).

### Library use

```go
srv := server.New(server.Options{Logger: logger}, myAgent, otherAgent)
http.ListenAndServe(":8080", srv.Handler())

server.ServeStdio(ctx, acp.Protocol, myAgent, os.Stdin, os.Stdout)
```

Library users get an in-memory store unless they pass one in `server.Options.Store`, so embedding rosetta never creates a database file by surprise.

## 8. Packages

```
rosetta/                 Agent, Card, Request, Message, Part, events, Stream, Func
server/                  composes adapters into one http.Handler, stdio entry point
protocol/                Adapter interfaces (the contributor surface)
protocol/mcp/            MCP server adapter (mcp-go)
protocol/a2a/            A2A adapter (a2a-go/v2), including a task store on top of store/
protocol/acp/            ACP adapter (acp-go-sdk)
protocol/agui/           AG-UI adapter (community SDK types and SSE writer)
registry/                live agent list: code agents, config agents, database agents
store/                   Store interface, in-memory store, SQL store
store/migrations/        embedded SQL migrations, one folder per dialect
admin/                   /admin/v1 handlers
api/admin/openapi.yaml   OpenAPI 3.1 description of the admin API
agents/llm/              config-declared agent: tool-calling loop over an OpenAI-compatible API
agents/echo/             echo agent for demos and end-to-end tests
llm/openai/              small OpenAI-compatible chat completions client with streaming and tools
tools/mcp/               tool source backed by an MCP server (airlock or any other)
config/                  YAML loading and ROSETTA_* environment overrides
internal/run/            runner: framing, session history, run records, panics, cancellation, metrics
internal/httpx/          auth, X-Request-ID, trace header capture and forwarding
cmd/rosetta/             serve, stdio and migrate commands
examples/                library examples, a sample rosetta.yaml, demo scripts
```

### The adapter interface

```go
type Runner interface {
	Run(ctx context.Context, agent rosetta.Agent, req *rosetta.Request, sink func(rosetta.Event) error) error
}

type Registry interface {
	Get(name string) (rosetta.Agent, bool)
	List() []rosetta.Agent
	Changes() <-chan struct{}
}

type HTTPAdapter interface {
	Name() string
	Mount(mux *http.ServeMux, agents Registry, run Runner) error
}

type StdioAdapter interface {
	Name() string
	ServeStdio(ctx context.Context, agent rosetta.Agent, run Runner, in io.Reader, out io.Writer) error
}
```

An adapter decodes a protocol request into a `rosetta.Request`, calls `run.Run`, and translates each framed event to the wire. Session history, identity capture, framing, run records, panics and metrics live in the runner, so a new adapter is mostly a mapping table turned into code. Adding a fifth protocol means a new `protocol/<name>` package, its golden tests, a row in `docs/protocols.md`, and one line in `server`.

## 9. The config-declared agent and the gateway clients

### Config

The YAML file is optional. With no file, rosetta starts with SQLite in its default location, no agents, and a generated admin key (section 11), ready to be set up from the console.

```yaml
listen_addr: ":8080"
default_agent: helper

storage:
  dsn: sqlite:///var/lib/rosetta/rosetta.db
  secret_key: "${ROSETTA_SECRET_KEY}"

auth:
  api_keys:
    - { name: ci, key: "${ROSETTA_CI_KEY}", role: user }

transponder:
  url: http://transponder:8080/v1
  api_key: ""

airlock:
  url: http://airlock:8080/mcp
  api_key: ""

protocols:
  mcp:  { enabled: true, path: /mcp }
  a2a:  { enabled: true, path: /a2a }
  agui: { enabled: true, path: /agui }

sessions:
  ttl: 24h

agents:
  - name: helper
    description: Answers questions and files GitHub issues.
    kind: llm
    model: claude-sonnet-5-5
    system_prompt: You are a helpful assistant.
    tools: ["github__*", "logbook__remember", "logbook__recall"]
    max_steps: 8
  - name: echo
    kind: echo
```

Any key can be overridden by environment: `ROSETTA_LISTEN_ADDR`, `ROSETTA_STORAGE_DSN`, `ROSETTA_TRANSPONDER_URL`, and so on (nested keys joined with `_`).

### Where a setting comes from

Every agent and every setting has a source: `code` (registered in Go), `config` (YAML or environment), or `api` (created through the admin API and stored in the database).

- Config wins. A value set in YAML or environment is shown in the admin API with `source: config` and cannot be changed there. Teams that keep config in git stay in control.
- Everything else is editable through the admin API and stored in the database.
- Agent names are unique across all sources. Creating an API agent with the name of a config agent fails with a conflict.
- Settings editable at runtime: `transponder` and `airlock` URL and key, `default_agent`, `sessions.ttl`, and whether each protocol is enabled. `listen_addr`, `storage` and protocol paths need a restart and are config only.

### `kind: llm`

A plain tool-calling loop:

1. Build messages from `system_prompt`, `History` and `Input`.
2. List tools from the MCP tool source once per run, filtered by the `tools` globs, and convert them to OpenAI function tools.
3. Call `/chat/completions` with `stream: true`. Stream text deltas to `out.Text`.
4. For each tool call: `out.ToolCall`, call the tool through MCP, `out.ToolResult`, append the result, and go back to step 3.
5. Stop when the model returns no tool calls, or after `max_steps` (then return an error saying the step limit was hit).

`transponder.url` can point at transponder, OpenAI, Ollama, or anything else that speaks OpenAI-compatible chat completions. `airlock.url` can point at airlock or any MCP server over Streamable HTTP. With no `airlock` set, the agent runs without tools.

The OpenAI-compatible client is written in-house (`llm/openai`, a few hundred lines) rather than imported, because rosetta only needs one endpoint with streaming and tools, and a full SDK would dwarf the rest of the binary.

### Identity and headers on outbound calls

Per the contract:

- The caller's `Authorization` header is forwarded unchanged to `airlock` and `transponder`. When there is no caller token (stdio mode), the configured `api_key` for that upstream is used.
- `traceparent` and `tracestate` are forwarded on every outbound call.
- `X-Request-ID` is kept from the inbound request, or generated, and sent upstream.
- `X-Usage-*` headers from transponder become a `Usage` event, which the runner stores on the run record and adds to metrics.

### Inbound auth in v0.1

- HTTP auth is on by default. Callers send `Authorization: Bearer <key>`.
- Keys come from config, or are created through the admin API. Each key has a role: `admin` (protocol endpoints and the admin API) or `user` (protocol endpoints only).
- `auth.disabled: true` turns auth off for local hacking. The admin API then only answers requests from loopback addresses.
- stdio has no auth, as the editor that spawned the process is the trust boundary.
- OIDC JWT checking arrives in v0.2, with a configurable claim that maps users to the `admin` role.

## 10. Storage

Per the contract, state that must survive a restart lives in SQL.

- **Drivers:** embedded SQLite through `modernc.org/sqlite` by default, Postgres through `pgx` (its `database/sql` driver) for teams and for several replicas. Both are pure Go, so builds need no cgo.
- **DSN:** `storage.dsn`, either `sqlite://<path>` or `postgres://...`. The default is `rosetta.db` under the user config directory (`os.UserConfigDir()/rosetta`), so `rosetta serve` and the `rosetta stdio` processes an editor spawns share one file. The Docker image sets it to `/data/rosetta.db`.
- **Migrations:** plain SQL files embedded in the binary, one folder per dialect, applied in order at startup and recorded in `schema_migrations`. On Postgres a migration run holds an advisory lock, so replicas starting together do not race. `rosetta migrate` runs them without starting the server, for teams that migrate as a separate deploy step. A database that is newer than the binary stops startup with a clear message.
- **SQLite settings:** WAL mode and a busy timeout, so several processes (one server and a few stdio children) can share the file.
- **Queries:** hand-written SQL over `database/sql` with a small helper for the placeholder difference between the two dialects. No ORM.

### What is stored

| Table | Holds |
|---|---|
| `agents` | agents created through the admin API: name, kind, spec as JSON, timestamps, who changed it |
| `settings` | runtime-editable settings, one row per key |
| `api_keys` | name, role, key prefix, SHA-256 hash of the key, created, last used, revoked |
| `sessions`, `messages` | conversation history per agent and session ID, with an expiry time |
| `a2a_tasks` | A2A task state with a version number, behind a2a-go's `taskstore.Store` (four methods) |
| `runs` | one row per run: agent, protocol, session, caller, status, error, start and end time, tokens, cost, request ID |
| `audit` | every change made through the admin API: when, who, action, target, details |
| `revision` | one counter, bumped on every change to agents or settings |

Notes:

- Upstream API keys stored through the admin API are encrypted with AES-GCM using `storage.secret_key`. If no secret key is configured, the admin API refuses to store secrets, and they must come from config or environment. Secrets are never returned by the API, only `"set": true`.
- Inbound API keys are stored only as hashes. The full key is shown once, when it is created.
- Expired sessions and runs older than `runs.retention` (default 30 days) are deleted by a background job.
- With several replicas on Postgres, each replica checks the `revision` counter every five seconds and reloads agents and settings when it moves. Sessions and A2A tasks are shared, so a client can land on any replica. A2A cancellation across replicas relies on a2a-go's optimistic versioning of the task store.
- The in-memory store implements the same interface. Tests and library users use it.

## 11. Admin API

Per the contract: JSON under `/admin/v1/`, described by `api/admin/openapi.yaml`, same identity rules as the rest of the API, and it covers every common task, not only reads. rosetta ships no UI.

### Endpoints

| Method and path | What |
|---|---|
| `GET /admin/v1/status` | version, uptime, storage dialect, enabled protocols, health of each upstream |
| `GET /admin/v1/config` | effective config, every field with its source, secrets shown as set or not set |
| `PATCH /admin/v1/settings` | change runtime-editable settings |
| `GET /admin/v1/agents` | list agents from all sources |
| `POST /admin/v1/agents` | create an agent |
| `GET /admin/v1/agents/{name}` | one agent, with its source and whether it is editable |
| `PUT /admin/v1/agents/{name}` | replace an API agent |
| `DELETE /admin/v1/agents/{name}` | delete an API agent |
| `GET /admin/v1/agents/{name}/endpoints` | ready-to-paste connection details: MCP URL, A2A card URL, AG-UI URL, and the ACP command line for an editor |
| `GET /admin/v1/tools` | tools available from airlock, for picking a tool allowlist |
| `GET /admin/v1/api-keys` | list keys (never the secret) |
| `POST /admin/v1/api-keys` | create a key. The response holds the key, shown once |
| `DELETE /admin/v1/api-keys/{id}` | revoke a key |
| `GET /admin/v1/runs` | run history, filtered by agent, protocol, status and time range |
| `GET /admin/v1/runs/{id}` | one run |
| `GET /admin/v1/sessions` | sessions, filtered by agent |
| `GET /admin/v1/sessions/{id}` | one session with its messages |
| `DELETE /admin/v1/sessions/{id}` | delete a session |
| `GET /admin/v1/usage` | tokens, cost and run counts, grouped by agent, protocol or day over a time range |
| `GET /admin/v1/audit` | audit records |

The `endpoints` call exists so the console can turn "connect this agent to Zed" or "add it to Claude Desktop" into one copy button.

### Conventions

- **Auth:** the `admin` role is required for every admin call.
- **Lists:** `cursor` and `limit` in (default 50, max 200), `items` and `next_cursor` out. An empty `next_cursor` means the end. Cursors are opaque, encoding the sort key and ID, so lists stay stable while rows are added.
- **Errors:** `{"error": {"code": "conflict", "message": "...", "field": "name"}}` with a matching HTTP status. `field` is set on validation errors so a form can point at the right input.
- **Concurrent edits:** `GET` on an agent returns an `ETag`. `PUT` and `DELETE` accept `If-Match` and answer `412` when the agent changed in the meantime, so two console tabs cannot silently overwrite each other.
- **Audit:** every write is recorded in `audit` with the caller's identity (key name now, OIDC subject in v0.2).

### First run without YAML

When the `api_keys` table is empty and config defines no keys, rosetta creates one `admin` key on first start, stores its hash, and prints the key once to the log. `ROSETTA_BOOTSTRAP_ADMIN_KEY` sets that first key instead, for Docker and CI. A new user pastes it into the console, sets the transponder URL, creates an agent, and copies a connection snippet, all without touching a file.

### Keeping the OpenAPI file honest

`api/admin/openapi.yaml` is written by hand and is the source of truth. A test loads it and checks that every operation in the file has a registered route and every route is in the file. Handler tests validate their responses against the schemas in the file.

## 12. Sessions and errors

### Sessions

- The store keeps short-term conversation history per `(agent, session ID)`, in SQL by default, with an expiry of `sessions.ttl`.
- AG-UI requests carry their own history, which rosetta uses as given and does not store.
- MCP calls without `session_id` are single-turn.
- Long-term memory is not rosetta's job. An agent that should remember things uses `logbook` tools through `airlock`.

### Errors

- An error from `Run` becomes the protocol's failure shape (table in section 6). The message text is passed through. Internal details stay in logs. The run record keeps the message and the status.
- A panic in an agent is recovered by the runner, logged with a stack trace, and reported as a failed run.
- A broken client connection cancels the run's context.
- An unreachable `transponder` or `airlock` fails the run with a message naming the upstream. `/readyz` reports each configured upstream and the database separately.
- A database error while saving history or a run record is logged and counted in metrics, but does not fail a run that has already produced its answer.
- Bad requests (unknown agent, invalid JSON, missing input) get the protocol's own error codes from the SDK.

## 13. Testing

Test-driven throughout, with `go test ./...`. No `-race`, since the build machine has no C compiler. CI on Linux can add `-race` later.

- **Core:** runner framing, session history, run records, panic recovery, cancellation. Table tests.
- **Adapters:** for each protocol, a scripted agent emits a fixed event sequence covering every event type. The adapter's output is compared to golden files, and the SDK's own client reads it back in-process.
- **Store:** one shared test suite run against the in-memory store and SQLite (a temp file) every time, and against Postgres when `ROSETTA_TEST_POSTGRES_DSN` is set (CI runs it with a Postgres service). Migration tests apply every migration to an empty database.
- **Admin API:** handler tests through `httptest`, responses validated against the OpenAPI file, plus the route parity test.
- **llm agent:** a fake OpenAI-compatible server (`httptest`) with scripted streaming replies, and an in-process mcp-go server as the tool source. Covers tool loops, `max_steps`, upstream errors and header forwarding.
- **Config:** YAML plus environment overrides, and the source rules in section 9.
- **Binary smoke test:** start `rosetta serve` with no config, read the bootstrap key, create an echo agent through the admin API, and hit all three HTTP protocols. Start `rosetta stdio --protocol acp` and drive it with the ACP client over pipes.
- Golden files are stored with LF line endings through `.gitattributes`, since the repository is developed on Windows with `core.autocrlf=true`.

## 14. First release (v0.1) scope

The plan builds this in four milestones, and the first two already make a demo:

1. Core types, runner, in-memory store, the four adapters, the echo agent, `server` and the `stdio` command.
2. The llm agent, the OpenAI-compatible client, the MCP tool source, config, auth with config keys, operations endpoints.
3. SQL storage: SQLite and Postgres, migrations, sessions, runs, A2A tasks, API agents, settings.
4. Admin API, OpenAPI file, bootstrap key, audit, usage.

Also in v0.1:

- `examples/`: a library example, a sample config, and a demo script for each protocol.
- README with the five-minute demo and the "everything is free" note, `docs/protocols.md`, `CONTRIBUTING.md` that points contributors at `protocol/`.
- Dockerfile and release builds for Linux, macOS and Windows.

Out of v0.1 (next, in rough order):

- OIDC JWT checking and OTLP trace export, both required by the contract before the mothership end-to-end test.
- Remote agents as a source: `kind: a2a` (and later `mcp` and `acp`) that wraps a remote agent and serves it over the other protocols. This is the "bridge" in the literal sense, so Zed can talk to any A2A agent over ACP.
- Input-required flows: A2A `input-required`, MCP elicitation, ACP permission requests.
- File and image parts.
- MCP tasks for long runs. AG-UI frontend tools. MCP servers passed by ACP clients in `session/new` exposed to the agent as tools.
- Per-agent access rules (which keys or users may call which agent), and multi-tenancy.
- A2A gRPC binding and push notifications.
- Framework bridges in `contrib/` (adk-go, eino, trpc-agent-go) as separate modules.

## 15. Dependencies

Go 1.26 or newer (`a2a-go/v2` needs 1.26). Direct dependencies in v0.1:

- `github.com/mark3labs/mcp-go`
- `github.com/a2aproject/a2a-go/v2`
- `github.com/coder/acp-go-sdk`
- `github.com/ag-ui-protocol/ag-ui/sdks/community/go` (pinned pseudo-version)
- `modernc.org/sqlite`
- `github.com/jackc/pgx/v5`
- `github.com/prometheus/client_golang`
- a YAML library (`go.yaml.in/yaml/v3`)

Test only: an OpenAPI loader and validator for the admin API tests.

Everything else is the standard library (`log/slog`, `net/http`, `database/sql`, `crypto`, `iter`).

## 16. Decisions taken and open choices

### Decisions made in this spec

1. Wrap the protocol SDKs inside one package per protocol. rosetta's own types are the only thing agents see.
2. mcp-go rather than the official MCP Go SDK, for both the MCP server and the airlock client.
3. AG-UI uses the community SDK's types and SSE writer with a hand-written handler, and can drop the SDK without touching anything outside `protocol/agui`.
4. Agents push events through a `Stream`. The run ends when `Run` returns.
5. The runner owns message framing, session history and run records, so adapters stay thin.
6. HTTP protocols share one listener. stdio is a separate mode that serves exactly one protocol, and shares the database with the server.
7. MCP exposes each agent as a tool named after the agent.
8. The OpenAI-compatible client is written in-house.
9. Agents and settings come from code, config or the admin API. Config wins and is read-only in the admin API.
10. SQLite by default, Postgres for teams, hand-written SQL, embedded migrations with a small in-house runner.
11. HTTP auth is on by default, with an `admin` and a `user` role, and a bootstrap admin key on first run. OIDC and OTLP export come next.
12. The admin API's OpenAPI file is hand-written and checked by tests, not generated.
13. The literal bridge (remote agents as a source) is the first feature after v0.1.

### Open choices for you

1. **mcp-go or the official go-sdk?** Picked mcp-go. If airlock and logbook end up on the official SDK, rosetta should probably match so the mothership repos share one MCP library.
2. **Bring the A2A bridge (`kind: a2a`) into v0.1?** It makes a stronger first demo (any A2A agent usable from Zed over ACP) for roughly one more adapter's worth of work.
3. **Contract note on stdio logging.** In stdio mode logs must go to stderr. airlock's stdio mode has the same issue. Suggest adding one line to `CONTRACT.md`: "In stdio mode, logs go to stderr."
4. **OIDC timing.** v0.2 as written. Moving it into v0.1 adds a JWKS library and roughly a day of work.
5. **Bootstrap admin key.** Printing a one-time key on first start is simple and works in Docker. The other option is an open admin API on loopback until the first key is created. All four projects should probably do the same thing, so it may belong in the contract.
6. **Hand-written OpenAPI or generated?** Hand-written with parity tests as written. Generating server stubs (for example with oapi-codegen) is stricter but adds a code generator to the build.
7. **Agent card details.** v0.1 advertises streaming, text and JSON input and output. Signed agent cards (supported by a2a-go) can come later if anyone asks.
