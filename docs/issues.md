# Milestone 1 issues

Milestone 1 from [design.md](design.md#14-first-release-v01-scope): core types, runner, in-memory store, the four protocol adapters, the echo agent, `server`, and the `serve` and `stdio` commands. Each section below is one GitHub issue, in the format from `mothership/contributing/labels.md`. The `Labels:` line becomes the issue's labels.

## Core types: Agent, Request, events and Stream

Labels: enhancement

### Why

Every other package depends on these types. They are the one interface agent authors write against, so they have to be small and stable.

### Proposed API

```go
package rosetta

type Agent interface {
	Card() Card
	Run(ctx context.Context, req *Request, out Stream) error
}

type Card struct{ Name, Description, Version string; Skills []Skill }
type Skill struct{ ID, Name, Description string; Tags, Examples []string }

type Request struct {
	SessionID, RunID string
	Input            []Part
	History          []Message
	State            json.RawMessage
	Caller           Caller
}
type Caller struct{ Token, RequestID string; Trace http.Header }
type Message struct{ Role Role; Parts []Part }
type Part struct{ Text string; Data json.RawMessage }

type Event interface{ isEvent() }
type TextDelta struct{ MessageID, Text string }
type ReasoningDelta struct{ Text string }
type ToolCall struct{ ID, Name string; Args json.RawMessage }
type ToolResult struct{ ID string; Result json.RawMessage; IsError bool }
type Step struct{ Name string; Finished bool }
type StateSnapshot struct{ State json.RawMessage }
type Artifact struct{ Name string; Parts []Part }
type Usage struct{ InputTokens, OutputTokens int64 }
type Extension struct{ Protocol string; Value any }

type Stream interface {
	Send(Event) error
	Text(s string) error
	Reasoning(s string) error
	ToolCall(id, name string, args json.RawMessage) error
	ToolResult(id string, result json.RawMessage, isError bool) error
}

func Func(card Card, run func(ctx context.Context, req *Request, out Stream) error) Agent
func NewStream(send func(Event) error) Stream
```

### Behavior

- `Event` is sealed: only the types above implement it.
- The `Stream` helpers build the matching event and call `Send`.
- `Send` returns the sink's error unchanged, so an agent can stop when the client is gone.
- `Func` returns an `Agent` whose `Card` and `Run` are the given values.
- `Role` has `RoleUser`, `RoleAgent` and `RoleSystem`.

### Tests

- Each `Stream` helper sends exactly one event of the right type with the right fields.
- `Send` passes the sink error through.
- `Func` returns the card and calls the function with the same request.

### Compatibility

New package.

## Runner: framing, history, panics and cancellation

Labels: enhancement

### Why

Adapters should only translate. The runner does the shared work once: message IDs, session history, panic recovery and cancellation.

### Proposed API

```go
package run

type Runner struct{ /* store, logger, id func */ }

func New(store session.Store, opts ...Option) *Runner
func (r *Runner) Run(ctx context.Context, agent rosetta.Agent, req *rosetta.Request, sink func(rosetta.Event) error) error
```

`internal/run`, implementing `protocol.Runner`.

### Behavior

- Fills `req.RunID` when it is empty.
- When `req.SessionID` is set and `req.History` is empty, loads history from the store. After a successful run, appends the user input and the agent's text to the session.
- Framing: every `TextDelta` gets a message ID. Consecutive text deltas share one ID. Any non-text event closes the current message, so the next text delta starts a new ID. An agent-supplied `MessageID` is kept.
- A panic in `Run` is recovered, logged with the stack, and returned as an error.
- If `ctx` is cancelled, the run returns `context.Canceled` even if the agent ignores the context.
- A sink error stops forwarding and is returned.

### Tests

- Table tests for framing: text only, text then tool call then text, agent-supplied IDs.
- History is loaded when missing and left alone when the protocol sent it.
- Panic becomes an error. Cancellation returns `context.Canceled`.

### Compatibility

New package.

## In-memory session store

Labels: enhancement, good first issue

### Why

The runner needs somewhere to keep short conversation history. The SQL store comes in milestone 3. Tests and library users need an in-memory one now.

### Proposed API

```go
package session

type Store interface {
	History(ctx context.Context, agent, sessionID string) ([]rosetta.Message, error)
	Append(ctx context.Context, agent, sessionID string, msgs ...rosetta.Message) error
	Delete(ctx context.Context, agent, sessionID string) error
}

func NewMemory(ttl time.Duration, now func() time.Time) *Memory
```

### Behavior

- Sessions are keyed by `(agent, sessionID)`. The same session ID under two agents is two sessions.
- A session expires `ttl` after its last `Append`. `History` on an expired or unknown session returns an empty slice and no error.
- `History` returns a copy, so callers cannot change stored messages.
- Safe for concurrent use.
- `now` is injected so tests never sleep.

### Tests

- Append then read back, in order.
- Two agents with the same session ID stay separate.
- Expiry, using a fake clock.
- Mutating the returned slice does not change the store.
- Concurrent appends from several goroutines.

### Where

`session/store.go` for the interface, `session/memory.go` and `session/memory_test.go`.

## Adapter interfaces and the golden test helper

Labels: enhancement

### Why

The `protocol` package is where contributors add things. It needs the interfaces every adapter implements and a test kit that makes wire output easy to check.

### Proposed API

```go
package protocol

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

package protocoltest

func ScriptedAgent(name string, events ...rosetta.Event) rosetta.Agent
func AllEvents() []rosetta.Event
func StaticRegistry(agents ...rosetta.Agent) protocol.Registry
```

### Behavior

- `ScriptedAgent` sends the given events in order and returns nil.
- `AllEvents` returns one of every event type, so each adapter's golden test covers the whole event set.

### Tests

- The scripted agent sends exactly the events given.
- `StaticRegistry` finds agents by name and lists them in order.

### Compatibility

New packages.

## Golden file helper with an -update flag

Labels: enhancement, good first issue

### Why

Each adapter compares its wire output with files in `testdata/`. A shared helper keeps that the same everywhere and lets a contributor refresh the files after a deliberate change.

### Proposed API

```go
package golden

func Assert(t testing.TB, name string, got []byte)
```

Run with `go test ./... -update` to rewrite the files.

### Behavior

- Reads `testdata/<name>.golden` and fails with a readable line diff when it differs from `got`.
- With `-update`, writes `got` to the file instead and passes.
- Normalises CRLF to LF before comparing, so a Windows checkout does not fail.
- A missing file fails with a message that tells you to run with `-update`.

### Tests

- Equal content passes. Different content fails and the message shows the differing lines.
- `-update` writes the file (use `t.TempDir()` and change directory in the test).
- CRLF and LF content compare equal.

### Where

`internal/golden/golden.go` and `internal/golden/golden_test.go`.

## MCP adapter

Labels: enhancement, adapter, help wanted

### Why

MCP is how Claude Desktop, Cursor and many agents call tools. Each rosetta agent becomes one MCP tool.

### Proposed API

`protocol/mcp`, built on `github.com/mark3labs/mcp-go`.

```go
func New(opts Options) *Adapter
func (a *Adapter) Mount(mux *http.ServeMux, agents protocol.Registry, run protocol.Runner) error
func (a *Adapter) ServeStdio(ctx context.Context, agent rosetta.Agent, run protocol.Runner, in io.Reader, out io.Writer) error
```

Tool input schema: `{"message": string (required), "session_id": string}`.

### Behavior

- One tool per agent, named after the agent, with the card description.
- `tools/call` builds a `rosetta.Request` with `message` as a text part and `session_id` as the session.
- With a progress token, text deltas, steps and tool calls are sent as `notifications/progress` messages.
- The final result holds the full text. `Artifact` data goes into `structuredContent`.
- An agent error returns a tool result with `isError: true` and the error message.
- `notifications/cancelled` cancels the run.
- When the registry changes, send `notifications/tools/list_changed`.
- Mapping per section 6 of the design.

### Tests

- Golden test of the JSON-RPC frames for `protocoltest.AllEvents()`.
- Round trip with the mcp-go client in-process over Streamable HTTP and over stdio pipes.
- Error result, cancellation, and the list-changed notification.

### Compatibility

New package.

## A2A adapter

Labels: enhancement, adapter, help wanted

### Why

A2A is how agents call other agents. Each rosetta agent gets its own agent card and endpoint.

### Proposed API

`protocol/a2a`, built on `github.com/a2aproject/a2a-go/v2` (`a2asrv`).

- `POST /a2a/{agent}` JSON-RPC, `/a2a/{agent}/v1/...` REST.
- `/a2a/{agent}/.well-known/agent-card.json`, and `/.well-known/agent-card.json` for the default agent.

### Behavior

- The executor emits `TASK_STATE_SUBMITTED`, then `TASK_STATE_WORKING`, before the first agent event.
- `TextDelta` becomes an artifact update on artifact `response` with `append: true`.
- `ToolCall`, `ToolResult` and `Step` become `working` status updates with a data part.
- `Run` returning nil ends with the last artifact chunk and `TASK_STATE_COMPLETED`. An error ends with `TASK_STATE_FAILED` and the message.
- `CancelTask` cancels the run's context and ends with `TASK_STATE_CANCELED`.
- `contextId` is the session ID. `taskId` is the run ID.
- The card advertises streaming, and text and JSON input and output modes.

### Tests

- Golden test of the streamed events for `protocoltest.AllEvents()`.
- Round trip with `a2aclient`: non-streaming send returns a completed task, streaming send gets every event, cancel works.
- Unknown agent returns the A2A not-found error.

### Compatibility

New package.

## ACP adapter

Labels: enhancement, adapter, help wanted

### Why

ACP is how editors such as Zed talk to agents. Editors start the agent as a child process and talk over stdio.

### Proposed API

`protocol/acp`, built on `github.com/coder/acp-go-sdk`.

```go
func New(opts Options) *Adapter
func (a *Adapter) ServeStdio(ctx context.Context, agent rosetta.Agent, run protocol.Runner, in io.Reader, out io.Writer) error
```

### Behavior

- `initialize` answers protocol version 1 with text prompts only and no session loading.
- `session/new` returns a fresh session ID. `session/prompt` runs the agent with that session.
- `TextDelta` becomes `agent_message_chunk`. `ReasoningDelta` becomes `agent_thought_chunk`.
- `ToolCall` becomes `tool_call`. `ToolResult` becomes `tool_call_update` with `completed` or `failed`.
- `Run` returning nil ends the prompt with stop reason `end_turn`. A cancelled run returns `cancelled`. An error returns a JSON-RPC error.
- `session/cancel` cancels the running prompt.
- Logs never go to `out`.

### Tests

- Golden test of the notifications for `protocoltest.AllEvents()`.
- Round trip with the SDK's `ClientSideConnection` over `io.Pipe`: initialize, new session, prompt, cancel.
- Two prompts in one session see the first one in `History`.

### Compatibility

New package.

## AG-UI adapter

Labels: enhancement, adapter, help wanted

### Why

AG-UI is how web front ends such as CopilotKit stream an agent's output into a page.

### Proposed API

`protocol/agui`, using the community SDK's `core/types`, `core/events` and `encoding/sse` packages with a hand-written handler.

- `POST /agui/{agent}` with a `RunAgentInput` body, answered with `text/event-stream`.

### Behavior

- Starts with `RUN_STARTED` (`threadId`, `runId`).
- `TextDelta` becomes `TEXT_MESSAGE_START`, `TEXT_MESSAGE_CONTENT` and `TEXT_MESSAGE_END`, using the runner's message IDs.
- `ReasoningDelta` becomes the `REASONING_*` events. `ToolCall` becomes `TOOL_CALL_START`, `TOOL_CALL_ARGS` and `TOOL_CALL_END`. `ToolResult` becomes `TOOL_CALL_RESULT`.
- `Step` becomes `STEP_STARTED` or `STEP_FINISHED`. `StateSnapshot` becomes `STATE_SNAPSHOT`. `Artifact` becomes a `CUSTOM` event named `artifact`.
- `Run` returning nil ends with `RUN_FINISHED`. An error ends with `RUN_ERROR`.
- `threadId` is the session ID. The messages in the request are used as `History` and are not stored.
- A client that closes the stream cancels the run.

### Tests

- Golden test of the SSE frames for `protocoltest.AllEvents()`.
- Read back with the community SDK's SSE client.
- Invalid JSON body returns 400. Unknown agent returns 404.

### Compatibility

New package.

## Echo agent

Labels: enhancement, good first issue

### Why

Demos and end-to-end tests need an agent that answers without any model or API key.

### Proposed API

```go
package echo

func New(name string) rosetta.Agent
```

Config: `kind: echo`.

### Behavior

- Card: the given name, description `Repeats what you send.`
- Sends one `Step{Name: "echo"}`, then the input text as one `TextDelta`, then `Step{Name: "echo", Finished: true}`.
- With several text parts, joins them with a newline.
- With no text in the input, returns an error `echo: no text in input`.

### Tests

- Single text part, several parts, empty input.
- Events come in the order above.

### Where

`agents/echo/echo.go` and `agents/echo/echo_test.go`.

## server: compose the adapters

Labels: enhancement

### Why

Library users need one call that gives them an `http.Handler` with every protocol mounted, and a stdio entry point.

### Proposed API

```go
package server

type Options struct {
	Logger    *slog.Logger
	Store     session.Store
	Protocols Protocols
}

func New(opts Options, agents ...rosetta.Agent) *Server
func (s *Server) Handler() http.Handler
func ServeStdio(ctx context.Context, p StdioProtocol, agent rosetta.Agent, in io.Reader, out io.Writer) error
```

### Behavior

- Mounts MCP, A2A and AG-UI on their default paths, each one switchable off.
- Uses an in-memory store when `Store` is nil.
- Serves `/healthz`.
- Two agents with the same name make `New` panic with a clear message, since that is a programming error.

### Tests

- Every enabled protocol answers on its path. A disabled one returns 404.
- Duplicate agent names panic.
- `ServeStdio` with the ACP protocol answers `initialize`.

### Compatibility

New package.

## The serve and stdio commands

Labels: enhancement

### Why

The binary is how people try rosetta without writing Go. Milestone 1 ships it with the echo agent only. Config files come in milestone 2.

### Proposed API

```
rosetta serve [--listen :8080] [--agent echo]...
rosetta stdio --protocol acp|mcp [--agent echo]
rosetta version
```

### Behavior

- `serve` starts the HTTP server with one echo agent per `--agent` flag (default one named `echo`) and shuts down cleanly on Ctrl+C.
- `stdio` serves one protocol on stdin and stdout. Logs go to stderr as JSON.
- Unknown protocol or missing agent exits with status 2 and a usage message.

### Tests

- Flag parsing, table-driven.
- `stdio --protocol acp` driven over pipes in a test answers `initialize`.
- `serve` starts on a random port in a test and answers `/healthz`.

### Compatibility

New command.

## rosetta version command

Labels: enhancement, good first issue

### Why

The bug report template asks for the output of `rosetta version`.

### Proposed API

```
rosetta version
```

Prints `rosetta <version> <go version> <os>/<arch>`, for example `rosetta v0.1.0 go1.26.3 linux/amd64`. The version comes from `rosetta.Version()`, set at build time with `-ldflags "-X github.com/raven-clown/rosetta.version=v0.1.0"`.

### Behavior

- Without a build-time version, prints `dev`.
- Writes to stdout and exits 0.

### Tests

- Output format with the default version and with a version set in the test.

### Where

`cmd/rosetta/version.go` and `cmd/rosetta/version_test.go`.

## docs/protocols.md support table

Labels: docs, good first issue

### Why

Users and contributors need one page that says which spec version each adapter targets and which events it maps or drops.

### Proposed API

`docs/protocols.md` with one table per protocol, taken from section 6 of the design, plus the SDK and spec version each adapter uses.

### Behavior

- One row per rosetta event, with what the protocol receives or `dropped`.
- A short "How to add a protocol" section pointing at `protocol/` and the golden helper.

### Tests

None. Docs only.

### Where

`docs/protocols.md`, and a link from `README.md`.

## Library example

Labels: docs, good first issue

### Why

The quickest way to understand rosetta is a twenty-line program that serves an agent over every protocol.

### Proposed API

`examples/hello/main.go`: a `rosetta.Func` agent that greets the user by the text it receives, served with `server.New` on `:8080`.

### Behavior

- `go run ./examples/hello` serves the agent on `/mcp`, `/a2a/hello` and `/agui/hello`.
- `examples/hello/README.md` shows one `curl` command per HTTP protocol.

### Tests

- `examples/hello/main_test.go` starts the handler with `httptest` and checks the AG-UI stream contains the greeting.

### Where

`examples/hello/`.
