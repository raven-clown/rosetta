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
2. A binary. You declare agents in YAML (a model, a system prompt, a tool allowlist) and run `rosetta serve --config rosetta.yaml`. No Go needed.

### Who it is for

- Go developers who already have agent logic (their own loop, or one built with a framework) and want it reachable from editors, chat apps, web UIs and other agents without writing four servers.
- Platform teams that want a config-only agent which uses tools behind `airlock` and models behind `transponder`.

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
- **AG-UI: the community SDK's `core/types`, `core/events` and `encoding/sse` packages, pinned to one pseudo-version.** rosetta writes its own small HTTP handler around them. If that SDK stalls or its dependencies grow, AG-UI events are flat JSON objects, and replacing the imported types with local structs is a one-package change. The golden tests in section 11 make that swap safe.

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
- **`Part` holds text or JSON data in v0.1.** Files and images come later (section 12).

### Events

```go
type TextDelta      struct{ MessageID, Text string }
type ReasoningDelta struct{ Text string }
type ToolCall       struct{ ID, Name string; Args json.RawMessage }
type ToolResult     struct{ ID string; Result json.RawMessage; IsError bool }
type Step           struct{ Name string; Finished bool }
type StateSnapshot  struct{ State json.RawMessage }
type Artifact       struct{ Name string; Parts []Part }
type Extension      struct{ Protocol string; Value any }
```

`Event` is a sealed interface over these types. `ToolCall` and `ToolResult` report tools the agent itself calls (for example through `airlock`), so clients can show them.

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
| `/healthz`, `/readyz`, `/metrics` | operations, per the contract |

Each protocol can be switched off or moved to another path in config.

Because the MCP endpoint is an ordinary Streamable HTTP server, `airlock` can list rosetta as one of its downstream servers. Every rosetta agent then shows up behind `airlock` as `rosetta__<agent>`, with airlock's login, permissions and audit in front.

### stdio: `rosetta stdio`

Only one protocol can own stdin and stdout, so stdio is a separate mode:

```
rosetta stdio --protocol acp --agent helper --config rosetta.yaml
rosetta stdio --protocol mcp --config rosetta.yaml
```

Editors launch this command as a child process. In stdio mode logs go to stderr, since stdout carries the protocol. The contract says logs are JSON on stdout, so this needs a one-line note in the contract (see section 14).

### Library use

```go
srv := server.New(server.Options{Logger: logger}, myAgent, otherAgent)
http.ListenAndServe(":8080", srv.Handler())

server.ServeStdio(ctx, acp.Protocol, myAgent, os.Stdin, os.Stdout)
```

## 8. Packages

```
rosetta/                 Agent, Card, Request, Message, Part, events, Stream, Func
server/                  composes adapters into one http.Handler, stdio entry point
protocol/                Adapter interfaces (the contributor surface)
protocol/mcp/            MCP server adapter (mcp-go)
protocol/a2a/            A2A adapter (a2a-go/v2)
protocol/acp/            ACP adapter (acp-go-sdk)
protocol/agui/           AG-UI adapter (community SDK types and SSE writer)
session/                 Store interface, in-memory store with TTL and size cap
agents/llm/              config-declared agent: tool-calling loop over an OpenAI-compatible API
agents/echo/             echo agent for demos and end-to-end tests
llm/openai/              small OpenAI-compatible chat completions client with streaming and tools
tools/mcp/               tool source backed by an MCP server (airlock or any other)
config/                  YAML loading and ROSETTA_* environment overrides
internal/run/            runner: framing, session history, panics, cancellation, metrics
internal/httpx/          auth, X-Request-ID, trace header capture and forwarding
cmd/rosetta/             serve and stdio commands
examples/                library examples, a sample rosetta.yaml, demo scripts
```

### The adapter interface

```go
type Runner interface {
	Run(ctx context.Context, agent rosetta.Agent, req *rosetta.Request, sink func(rosetta.Event) error) error
}

type HTTPAdapter interface {
	Name() string
	Mount(mux *http.ServeMux, agents []rosetta.Agent, run Runner) error
}

type StdioAdapter interface {
	Name() string
	ServeStdio(ctx context.Context, agent rosetta.Agent, run Runner, in io.Reader, out io.Writer) error
}
```

An adapter decodes a protocol request into a `rosetta.Request`, calls `run.Run`, and translates each framed event to the wire. Session history, identity capture, framing, panics and metrics live in the runner, so a new adapter is mostly a mapping table turned into code. Adding a fifth protocol means a new `protocol/<name>` package, its golden tests, a row in `docs/protocols.md`, and one line in `server`.

## 9. The config-declared agent and the gateway clients

### Config

```yaml
listen_addr: ":8080"
default_agent: helper

auth:
  api_keys: ["${ROSETTA_API_KEY}"]

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
  ttl: 1h
  max: 10000

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

Any key can be overridden by environment: `ROSETTA_LISTEN_ADDR`, `ROSETTA_TRANSPONDER_URL`, `ROSETTA_AIRLOCK_URL`, and so on (nested keys joined with `_`).

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

- The caller's `Authorization` header is forwarded unchanged to `airlock` and `transponder`. When there is no caller token (stdio mode, or auth turned off), the configured `api_key` for that upstream is used.
- `traceparent` and `tracestate` are forwarded on every outbound call.
- `X-Request-ID` is kept from the inbound request, or generated, and sent upstream.
- `X-Usage-*` headers from transponder are added to rosetta's metrics per agent.

### Inbound auth in v0.1

`auth.api_keys` (static keys) or no auth. OIDC JWT checking arrives in v0.2. stdio has no auth, as the editor that spawned the process is the trust boundary.

## 10. Sessions and errors

### Sessions

- `session.Store` keeps short-term conversation history per `(agent, session ID)`. v0.1 ships an in-memory store with a TTL and a size cap. The interface allows a persistent store later.
- AG-UI requests carry their own history, which rosetta uses as given and does not store.
- MCP calls without `session_id` are single-turn.
- Long-term memory is not rosetta's job. An agent that should remember things uses `logbook` tools through `airlock`.

### Errors

- An error from `Run` becomes the protocol's failure shape (table in section 6). The message text is passed through. Internal details stay in logs.
- A panic in an agent is recovered by the runner, logged with a stack trace, and reported as a failed run.
- A broken client connection cancels the run's context.
- An unreachable `transponder` or `airlock` fails the run with a message naming the upstream. `/readyz` reports each configured upstream separately.
- Bad requests (unknown agent, invalid JSON, missing input) get the protocol's own error codes from the SDK.

## 11. Testing

Test-driven throughout, with `go test ./...`. No `-race`, since the build machine has no C compiler. CI on Linux can add `-race` later.

- **Core:** runner framing, session history, panic recovery, cancellation. Table tests.
- **Adapters:** for each protocol, a scripted agent emits a fixed event sequence covering every event type. The adapter's output is compared to golden files, and the SDK's own client reads it back in-process.
- **llm agent:** a fake OpenAI-compatible server (`httptest`) with scripted streaming replies, and an in-process mcp-go server as the tool source. Covers tool loops, `max_steps`, upstream errors and header forwarding.
- **Config:** YAML plus environment overrides.
- **Binary smoke test:** start `rosetta serve` with the echo agent and hit all three HTTP protocols. Start `rosetta stdio --protocol acp` and drive it with the ACP client over pipes.
- Golden files are stored with LF line endings through `.gitattributes`, since the repository is developed on Windows with `core.autocrlf=true`.

## 12. First release (v0.1) scope

In:

- Core types, `Func`, runner with framing, in-memory sessions.
- Four adapters: MCP (Streamable HTTP and stdio), A2A (JSON-RPC and REST), ACP (stdio), AG-UI (SSE).
- `kind: llm` and `kind: echo` agents.
- MCP tool source for `airlock`, OpenAI-compatible client for `transponder`.
- Static API key auth, `X-Request-ID`, trace header forwarding.
- YAML config with environment overrides, JSON logs via `slog`, `/healthz`, `/readyz`, `/metrics` (Prometheus).
- `examples/`: a library example, a sample config, and a demo script for each protocol.
- README with the five-minute demo, `docs/protocols.md`, `CONTRIBUTING.md` that points contributors at `protocol/`.
- Dockerfile and release builds for Linux, macOS and Windows.

Out of v0.1 (next, in rough order):

- OIDC JWT checking and OTLP trace export, both required by the contract before the mothership end-to-end test.
- Remote agents as a source: `kind: a2a` (and later `mcp` and `acp`) that wraps a remote agent and serves it over the other protocols. This is the "bridge" in the literal sense, so Zed can talk to any A2A agent over ACP.
- Input-required flows: A2A `input-required`, MCP elicitation, ACP permission requests.
- File and image parts.
- MCP tasks for long runs. AG-UI frontend tools. MCP servers passed by ACP clients in `session/new` exposed to the agent as tools.
- Persistent session stores. A2A gRPC binding and push notifications.
- Framework bridges in `contrib/` (adk-go, eino, trpc-agent-go) as separate modules.

## 13. Dependencies

Go 1.26 or newer (`a2a-go/v2` needs 1.26). Direct dependencies in v0.1:

- `github.com/mark3labs/mcp-go`
- `github.com/a2aproject/a2a-go/v2`
- `github.com/coder/acp-go-sdk`
- `github.com/ag-ui-protocol/ag-ui/sdks/community/go` (pinned pseudo-version)
- `github.com/prometheus/client_golang`
- a YAML library (`go.yaml.in/yaml/v3`)

Everything else is the standard library (`log/slog`, `net/http`, `iter`).

## 14. Decisions taken and open choices

### Decisions made in this spec

1. Wrap the protocol SDKs inside one package per protocol. rosetta's own types are the only thing agents see.
2. mcp-go rather than the official MCP Go SDK, for both the MCP server and the airlock client.
3. AG-UI uses the community SDK's types and SSE writer with a hand-written handler, and can drop the SDK without touching anything outside `protocol/agui`.
4. Agents push events through a `Stream`. The run ends when `Run` returns.
5. The runner owns message framing and session history, so adapters stay thin.
6. HTTP protocols share one listener. stdio is a separate mode that serves exactly one protocol.
7. MCP exposes each agent as a tool named after the agent.
8. The OpenAI-compatible client is written in-house.
9. v0.1 auth is static API keys. OIDC and OTLP export come next.
10. The literal bridge (remote agents as a source) is the first feature after v0.1.

### Open choices for you

1. **mcp-go or the official go-sdk?** Picked mcp-go. If airlock and logbook end up on the official SDK, rosetta should probably match so the mothership repos share one MCP library.
2. **Bring the A2A bridge (`kind: a2a`) into v0.1?** It makes a stronger first demo (any A2A agent usable from Zed over ACP) for roughly one more adapter's worth of work.
3. **Contract note on stdio logging.** In stdio mode logs must go to stderr. airlock's stdio mode has the same issue. Suggest adding one line to `CONTRACT.md`: "In stdio mode, logs go to stderr."
4. **OIDC timing.** v0.2 as written. Moving it into v0.1 adds a JWKS library and roughly a day of work.
5. **Agent card details.** v0.1 advertises streaming, text and JSON input and output. Signed agent cards (supported by a2a-go) can come later if anyone asks.
