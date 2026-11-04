# Milestone 1 Implementation Plan

**Goal:** One Go agent value served over MCP (Streamable HTTP and stdio), A2A (JSON-RPC and REST), ACP (stdio) and AG-UI (SSE), with an echo agent and a `rosetta` binary to demo it.

**Architecture:** Root package `rosetta` holds the types agents write against. `internal/run` frames events and keeps session history. Each protocol lives in `protocol/<name>` and is the only place its SDK is imported. `server` composes adapters into one `http.Handler` and a stdio entry point, and `cmd/rosetta` wraps it.

**Tech Stack:** Go 1.26, `github.com/mark3labs/mcp-go` v1.5.0, `github.com/a2aproject/a2a-go/v2` v2.7.0, `github.com/coder/acp-go-sdk` v0.13.5, `github.com/ag-ui-protocol/ag-ui/sdks/community/go` (pseudo-version `v0.0.0-20261104050642-08eae9dc1354`), standard library otherwise.

**Spec:** `docs/design.md` (sections 5, 6, 7, 8 and 13). Issues 1 to 15 on GitHub match the tasks below.

## Global Constraints

- Module `github.com/raven-clown/rosetta`, `go 1.26` in `go.mod`.
- Only `protocol/<name>` imports a protocol SDK in this milestone. No SDK type in any exported signature outside `protocol/`.
- Tests are table-driven, use no network beyond `httptest` and in-process pipes, and never call `time.Sleep`.
- Local test command is `go test ./...` (no C compiler, so no `-race`). CI adds `-race` on Linux and macOS.
- Coverage of 80% or more in total. `golangci-lint run ./...` clean.
- Golden files live in each package's `testdata/` with LF endings.
- Agent names match `^[a-z0-9][a-z0-9_-]{0,63}$`, because they become MCP tool names and URL path segments.
- Logs are `log/slog` JSON. In stdio mode they go to stderr, never to the protocol stream.
- Minimal comments, no em-dashes, Conventional Commits, commit and push after every task, and update AGENTS.md in the same commit when commands or layout change.

## Review Focus

1. **Client gone mid-stream.** An AG-UI or A2A streaming client disconnects while the agent is still sending. Expected: the run context is cancelled, `Stream.Send` returns an error, the handler returns, nothing panics. Test in Task 7 and Task 9.
2. **Events from several goroutines.** An agent calls `out.Text` from parallel goroutines. Expected: no interleaved half-events and stable framing. Test in Task 5.
3. **Events after `Run` returned.** A goroutine the agent leaked keeps calling `out.Text`. Expected: `Send` returns `rosetta.ErrRunEnded`, no panic, nothing reaches the wire. Test in Task 5.
4. **Empty or non-text input.** MCP `message` is empty, an A2A message has only a data part, an AG-UI run has no user message. Expected: data parts reach the agent as `Part.Data`. An empty input reaches the agent as an empty `Input`, and the echo agent then fails with its own error. Tests in Tasks 6, 8 and 9.
5. **Bad agent names.** A name with spaces, uppercase, a slash or over 64 characters, or two agents with one name. Expected: `registry.New` returns an error naming the agent, and `server.New` panics with that error. Test in Task 4.

---

### Task 1: Core types, events and Stream (issue 1)

**Files:**
- Create: `rosetta.go`, `event.go`, `stream.go`, `func.go`
- Test: `event_test.go`, `stream_test.go`, `func_test.go`
- Keep: `doc.go`, `version.go`

**Interfaces:**
- Produces: everything in design section 5, exactly as written there, plus:
  - `type Role string` with `RoleUser = "user"`, `RoleAgent = "agent"`, `RoleSystem = "system"`.
  - `var ErrRunEnded = errors.New("rosetta: run has ended")`.
  - `func NewStream(send func(Event) error) Stream`.
  - `func Func(card Card, run func(ctx context.Context, req *Request, out Stream) error) Agent`.
  - `func (r *Request) Text() string`: text parts of `Input` joined with `"\n"`.

- [ ] **Step 1: Write the failing tests**

`TestStreamHelpers` (table: `Text("hi")` sends `TextDelta{Text: "hi"}`, `Reasoning`, `ToolCall("t1","search",{"q":1})`, `ToolResult("t1",{"ok":true},false)`), each case asserting exactly one event with equal fields via `reflect.DeepEqual`. `TestStreamSendError`: sink returns `errBoom`, every helper returns `errBoom` (`errors.Is`). `TestFunc`: card round-trips, request pointer is the same one passed in. `TestRequestText`: no parts gives `""`, two text parts and one data part gives `"a\nb"`.

- [ ] **Step 2: Run them and see them fail**

Run: `go test . -run 'TestStream|TestFunc|TestRequestText'`
Expected: build failure, undefined `NewStream`.

- [ ] **Step 3: Implement the types**

Sealed `Event` via an unexported `isEvent()` method on each event struct.

- [ ] **Step 4: Run the tests**

Run: `go test .`
Expected: `ok`, coverage 100% for the root package.

- [ ] **Step 5: Commit and push**

`git commit -m "feat: add the agent interface, events and Stream"`

### Task 2: In-memory session store (issue 3)

**Files:**
- Create: `session/store.go`, `session/memory.go`
- Test: `session/memory_test.go`

**Interfaces:**
- Consumes: `rosetta.Message`.
- Produces: `session.Store` (design issue 3 signature) and `func NewMemory(ttl time.Duration, now func() time.Time) *Memory`. `now == nil` means `time.Now`.

- [ ] **Step 1: Write the failing tests**

`TestMemoryAppendAndHistory` (table: one append, two appends keep order), `TestMemorySeparatesAgents`, `TestMemoryExpiry` (fake clock: append at t0, ttl 1h, history at t0+59m has the message, at t0+61m is empty with nil error), `TestMemoryHistoryIsACopy`, `TestMemoryConcurrentAppends` (50 goroutines append one message each, history has 50), `TestMemoryDelete`.

- [ ] **Step 2: Run them and see them fail**

Run: `go test ./session/`
Expected: build failure.

- [ ] **Step 3: Implement `Memory`**

A `sync.Mutex` over `map[key]*entry` where `entry` holds messages and the last-append time. Expired entries are dropped lazily on read and append.

- [ ] **Step 4: Run the tests**

Run: `go test ./session/`
Expected: `ok`.

- [ ] **Step 5: Commit and push**

`git commit -m "feat: add the in-memory session store"`

### Task 3: Golden file helper (issue 5)

**Files:**
- Create: `internal/golden/golden.go`
- Test: `internal/golden/golden_test.go`

**Interfaces:**
- Produces: `func Assert(t testing.TB, name string, got []byte)` and a package-level `-update` flag (`flag.Bool("update", false, ...)`).

- [ ] **Step 1: Write the failing tests**

Use a fake `testing.TB` (embed `testing.TB`, record `Errorf`/`Fatalf` calls) so a failing comparison can be asserted. Cases: equal passes, different content fails and the message contains `-want` and `+got` lines for the differing line, CRLF file vs LF bytes passes, missing file fails with a message containing `-update`, and with `*update = true` the file is written under `t.TempDir()`.

- [ ] **Step 2: Run them and see them fail**

Run: `go test ./internal/golden/`
Expected: build failure.

- [ ] **Step 3: Implement `Assert`**

Path is `filepath.Join("testdata", name+".golden")`. Line diff: report each line index where want and got differ, prefixed `-want` and `+got`. Write with `os.MkdirAll` then `os.WriteFile(..., 0o644)`.

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/golden/`
Expected: `ok`.

- [ ] **Step 5: Commit and push**

`git commit -m "test: add a golden file helper"`

### Task 4: Registry, adapter interfaces and protocoltest (issue 4)

**Files:**
- Create: `registry/registry.go`, `protocol/protocol.go`, `protocol/protocoltest/protocoltest.go`
- Test: `registry/registry_test.go`, `protocol/protocoltest/protocoltest_test.go`

**Interfaces:**
- Consumes: `rosetta.Agent`, `rosetta.Event`.
- Produces:
  - `registry.New(agents ...rosetta.Agent) (*Registry, error)` with `Get(name string) (rosetta.Agent, bool)`, `List() []rosetta.Agent` (registration order), `Changes() <-chan struct{}` (never fires in this milestone), and `func ValidName(name string) bool`.
  - `protocol.Runner`, `protocol.Registry`, `protocol.HTTPAdapter`, `protocol.StdioAdapter`, exactly as in design section 8.
  - `protocoltest.ScriptedAgent(name string, events ...rosetta.Event) rosetta.Agent`, `protocoltest.AllEvents() []rosetta.Event`, `protocoltest.StaticRegistry(agents ...rosetta.Agent) protocol.Registry` (calls `registry.New`, panics on error).

- [ ] **Step 1: Write the failing tests**

`TestNewRejectsBadNames` (table: `""`, `"Echo"`, `"has space"`, `"a/b"`, 65 characters, each gives an error whose message contains the name), `TestNewRejectsDuplicates`, `TestGetAndList`. `TestScriptedAgentSendsEvents`: collect via `rosetta.NewStream`, equal to the input slice. `TestAllEventsCoversEveryType`: the set of `%T` values has 9 entries, one per event type in Task 1.

- [ ] **Step 2: Run them and see them fail**

Run: `go test ./registry/ ./protocol/...`
Expected: build failure.

- [ ] **Step 3: Implement the three packages**

`AllEvents` order: `Step{Name:"plan"}`, `ReasoningDelta`, `TextDelta` ×2, `ToolCall`, `ToolResult`, `TextDelta`, `StateSnapshot`, `Artifact` with one text and one data part, `Usage`, `Extension{Protocol:"none"}`, `Step{Name:"plan", Finished:true}`. Fixed IDs and values so golden files are stable.

- [ ] **Step 4: Run the tests**

Run: `go test ./registry/ ./protocol/...`
Expected: `ok`.

- [ ] **Step 5: Commit and push**

`git commit -m "feat: add the registry, adapter interfaces and test kit"`

### Task 5: Runner (issue 2)

**Files:**
- Create: `internal/run/run.go`, `internal/run/frame.go`
- Test: `internal/run/run_test.go`, `internal/run/frame_test.go`

**Interfaces:**
- Consumes: `session.Store`, `rosetta.*`, `protocol.Runner`.
- Produces: `run.New(store session.Store, opts ...Option) *Runner`, `run.WithLogger(*slog.Logger) Option`, `run.WithIDs(func() string) Option` (default: 16 random bytes from `crypto/rand`, hex), and `(*Runner).Run` satisfying `protocol.Runner`.

- [ ] **Step 1: Write the failing tests**

`TestFraming` (table, IDs from a counter `m1, m2...`):
- `Text a, Text b` gives two `TextDelta` with `MessageID "m1"`.
- `Text a, ToolCall, Text b` gives `m1`, the tool call, then `m2`.
- an agent-supplied `MessageID "x"` is kept.

`TestRunFillsRunID`, `TestHistoryLoadedWhenMissing` (store has one message for `("a","s1")`, agent sees it in `req.History`), `TestHistoryKeptWhenSent`, `TestHistoryAppendedAfterSuccess` (store then holds the user input and one `RoleAgent` message with the joined text), `TestNoAppendOnError`, `TestPanicBecomesError` (error message contains `panic`), `TestCancelReturnsCanceled` (agent blocks on a channel and ignores ctx, cancel, `errors.Is(err, context.Canceled)`), `TestSinkErrorStops`.

Review Focus 2: `TestConcurrentSendsAreSerialised` (10 goroutines each send 100 `Text("x")`, the sink sees 1000 events, all with one message ID, and the sink is never entered concurrently: guard it with an atomic counter that must stay at most 1).
Review Focus 3: `TestSendAfterRunEnded` (agent stores `out`, returns nil; a later `out.Text("late")` returns `rosetta.ErrRunEnded` and the sink sees nothing).

- [ ] **Step 2: Run them and see them fail**

Run: `go test ./internal/run/`
Expected: build failure.

- [ ] **Step 3: Implement the runner**

The agent runs in its own goroutine so `Run` can return on cancellation. One mutex guards the framer and the sink. A `done` flag set under that mutex makes later sends return `ErrRunEnded`. `recover()` in the agent goroutine.

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/run/`
Expected: `ok`.

- [ ] **Step 5: Commit and push**

`git commit -m "feat: add the runner with framing and session history"`

### Task 6: Echo agent (issue 10)

**Files:**
- Create: `agents/echo/echo.go`
- Test: `agents/echo/echo_test.go`

**Interfaces:**
- Consumes: `rosetta.Func`, `(*rosetta.Request).Text`.
- Produces: `echo.New(name string) rosetta.Agent`.

- [ ] **Step 1: Write the failing tests**

`TestEcho` (table: `["hi"]` sends `Step{echo}`, `TextDelta{"hi"}`, `Step{echo, Finished}`; `["a","b"]` sends `"a\nb"`). `TestEchoEmptyInput`: no parts, and a single data part, both return an error equal to `echo: no text in input`. `TestEchoCard`: name as given, description `Repeats what you send.`

- [ ] **Step 2: Run them and see them fail**

Run: `go test ./agents/echo/`
Expected: build failure.

- [ ] **Step 3: Implement `New`**

- [ ] **Step 4: Run the tests**

Run: `go test ./agents/echo/`
Expected: `ok`.

- [ ] **Step 5: Commit and push**

`git commit -m "feat: add the echo agent"`

### Task 7: AG-UI adapter (issue 9)

**Files:**
- Create: `protocol/agui/agui.go`, `protocol/agui/convert.go`
- Test: `protocol/agui/agui_test.go`, `protocol/agui/testdata/all_events.golden`

**Interfaces:**
- Consumes: `protocol.HTTPAdapter`, `protocoltest`, `golden.Assert`, `run.New` with `session.NewMemory` in tests.
- Produces: `agui.New(opts Options) *Adapter` with `Options{Path string}` (default `/agui`), `Name() == "agui"`, and `Mount` registering `POST {Path}/{agent}`.

- [ ] **Step 1: Write the failing tests**

`TestAllEventsGolden`: scripted agent with `AllEvents()`, POST a `RunAgentInput` with `threadId "t1"`, `runId "r1"`, one user message `"hi"`; the response body matches `all_events`. `TestRoundTripWithSDKClient`: read the stream with the community SDK's `client/sse` and check the event types in order start with `RUN_STARTED` and end with `RUN_FINISHED`. `TestRunError`: agent returns `errors.New("nope")`, last event `RUN_ERROR` with message `nope`. `TestBadRequests` (table: invalid JSON gives 400, unknown agent gives 404, GET gives 405). `TestHistoryFromMessages`: two prior messages in the input reach `req.History`, and the last user message becomes `req.Input`.

Review Focus 1: `TestClientDisconnectCancelsRun` (agent sends one `Text`, then blocks on `ctx.Done()` and records it; the test reads the first frame, cancels the request context, and asserts the agent saw cancellation and the handler returned).
Review Focus 4: `TestNoUserMessage` (empty `messages`: agent receives empty `Input`, run still starts).

- [ ] **Step 2: Run them and see them fail**

Run: `go test ./protocol/agui/`
Expected: build failure.

- [ ] **Step 3: Implement the adapter**

Mapping per design section 6. `Usage` and `Extension` with another protocol name are dropped. `Extension{Protocol:"agui"}` writes `Value` as a raw event. Set `Content-Type: text/event-stream`, flush after each frame. Run the golden test once with `-update`, read the file by eye against section 6, then commit it.

- [ ] **Step 4: Run the tests**

Run: `go test ./protocol/agui/`
Expected: `ok`.

- [ ] **Step 5: Commit and push**

`git commit -m "feat: add the AG-UI adapter"`

### Task 8: MCP adapter (issue 6)

**Files:**
- Create: `protocol/mcp/mcp.go`, `protocol/mcp/tool.go`
- Test: `protocol/mcp/mcp_test.go`, `protocol/mcp/testdata/all_events.golden`

**Interfaces:**
- Consumes: as Task 7.
- Produces: `mcp.New(opts Options) *Adapter` with `Options{Path string, Name string, Version string}` (defaults `/mcp`, `rosetta`, `rosetta.Version()`), `Name() == "mcp"`, `Mount`, and `ServeStdio` (via `server.NewStdioServer(...).Listen(ctx, in, out)`).

- [ ] **Step 1: Write the failing tests**

`TestListTools`: two agents give two tools with the agents' names, descriptions and the input schema `{"message": string, "session_id": string}` with `message` required. `TestCallTool` (in-process client `client.NewInProcessClient`): echo-like scripted agent, result text equals the joined text deltas. `TestProgressGolden`: call with a progress token and `AllEvents()`; the recorded progress notifications plus the final result, marshalled as JSON lines, match `all_events`. `TestAgentErrorIsToolError`: `isError: true`, text `nope`. `TestSessionID`: two calls with `session_id "s1"` and the second sees the first in `History`. `TestStreamableHTTP`: the same call through `httptest.NewServer` and the Streamable HTTP client. `TestStdio`: `ServeStdio` over `io.Pipe` answers `tools/list`. `TestCancel`: `notifications/cancelled` stops a blocking agent.

Review Focus 4: `TestEmptyMessage` (`message ""`: the agent receives an empty `Input`; with the echo agent the tool result is an error containing `no text in input`).

- [ ] **Step 2: Run them and see them fail**

Run: `go test ./protocol/mcp/`
Expected: build failure.

- [ ] **Step 3: Implement the adapter**

`server.NewMCPServer(name, version, server.WithToolCapabilities(true))`, one `ServerTool` per registry entry via `SetTools`, mounted with `server.NewStreamableHTTPServer(s, server.WithEndpointPath(path))`. Mapping per design section 6.

- [ ] **Step 4: Run the tests**

Run: `go test ./protocol/mcp/`
Expected: `ok`.

- [ ] **Step 5: Commit and push**

`git commit -m "feat: add the MCP adapter"`

### Task 9: A2A adapter (issue 7)

**Files:**
- Create: `protocol/a2a/a2a.go`, `protocol/a2a/executor.go`, `protocol/a2a/card.go`
- Test: `protocol/a2a/a2a_test.go`, `protocol/a2a/testdata/all_events.golden`

**Interfaces:**
- Consumes: as Task 7.
- Produces: `a2a.New(opts Options) *Adapter` with `Options{Path string, BaseURL string, DefaultAgent string}` (default path `/a2a`; empty `DefaultAgent` means the first agent), `Name() == "a2a"`, `Mount` registering `POST {Path}/{agent}` (JSON-RPC), `{Path}/{agent}/v1/` (REST), `{Path}/{agent}/.well-known/agent-card.json` and `/.well-known/agent-card.json`.

- [ ] **Step 1: Write the failing tests**

`TestAgentCard`: name, description, skills from the `rosetta.Card`, streaming capability true, input and output modes `text/plain` and `application/json`, JSON-RPC and REST interfaces under `BaseURL`. `TestDefaultCard`. `TestSendMessage` (`a2aclient`, non-streaming): task state `TASK_STATE_COMPLETED`, artifact `response` holds the full text. `TestStreamGolden`: streaming send with `AllEvents()`, events as JSON lines with timestamps replaced by `"T"`, match `all_events`. `TestFailed`: `TASK_STATE_FAILED` with the message. `TestCancelTask`. `TestContextIsSession`: two messages with one `contextId`, the second run sees history. `TestUnknownAgent`: not-found error.

Review Focus 1: `TestStreamClientGone` (cancel the client context after the first event; the agent sees cancellation).
Review Focus 4: `TestDataPartOnly` (a message with only a data part reaches the agent as one `Part` with `Data` set).

- [ ] **Step 2: Run them and see them fail**

Run: `go test ./protocol/a2a/`
Expected: build failure.

- [ ] **Step 3: Implement the adapter**

An `a2asrv.AgentExecutor` per agent whose `Execute` returns an `iter.Seq2[a2a.Event, error]` fed from `run.Run` through a channel, `a2asrv.NewHandler(executor)`, then `a2asrv.NewJSONRPCHandler` and `a2asrv.NewRESTHandler`. In-memory task store in this milestone. Mapping per design section 6.

- [ ] **Step 4: Run the tests**

Run: `go test ./protocol/a2a/`
Expected: `ok`.

- [ ] **Step 5: Commit and push**

`git commit -m "feat: add the A2A adapter"`

### Task 10: ACP adapter (issue 8)

**Files:**
- Create: `protocol/acp/acp.go`, `protocol/acp/convert.go`
- Test: `protocol/acp/acp_test.go`, `protocol/acp/testdata/all_events.golden`

**Interfaces:**
- Consumes: as Task 7.
- Produces: `acp.New(opts Options) *Adapter` with `Options{Logger *slog.Logger}`, `Name() == "acp"`, `ServeStdio` using `acpsdk.NewAgentSideConnection(agentImpl, out, in)` and returning when `ctx` ends or the connection's `Done()` closes.

- [ ] **Step 1: Write the failing tests**

Drive it with `acpsdk.NewClientSideConnection` over two `io.Pipe`s and a test client that records `SessionUpdate` notifications. `TestInitialize`: protocol version 1, `loadSession` false. `TestPromptGolden`: `AllEvents()`, recorded updates as JSON lines match `all_events`, stop reason `end_turn`. `TestPromptError`: JSON-RPC error with the message. `TestCancel`: `session/cancel` during a blocking run gives stop reason `cancelled`. `TestSessionHistory`: second prompt sees the first. `TestUnknownSession`: prompt with an unknown session ID is an invalid-params error.

- [ ] **Step 2: Run them and see them fail**

Run: `go test ./protocol/acp/`
Expected: build failure.

- [ ] **Step 3: Implement the adapter**

Methods the agent does not support (`authenticate`, `logout`, `session/load`, unstable ones) return method-not-found. `ToolCall` uses kind `other` and status `in_progress`. Mapping per design section 6.

- [ ] **Step 4: Run the tests**

Run: `go test ./protocol/acp/`
Expected: `ok`.

- [ ] **Step 5: Commit and push**

`git commit -m "feat: add the ACP adapter"`

### Task 11: server package (issue 11)

**Files:**
- Create: `server/server.go`, `server/stdio.go`
- Test: `server/server_test.go`

**Interfaces:**
- Consumes: `registry.New`, `run.New`, `session.NewMemory`, the four adapters.
- Produces:
  - `server.Options{Logger *slog.Logger; Store session.Store; DefaultAgent string; BaseURL string; Protocols Protocols}` with `Protocols{MCP, A2A, AGUI Toggle}` and `Toggle{Disabled bool; Path string}`.
  - `server.New(opts Options, agents ...rosetta.Agent) *Server` (panics on a registry error), `(*Server).Handler() http.Handler`.
  - `type StdioProtocol string` with `ACP = "acp"` and `MCP = "mcp"`; `server.ServeStdio(ctx context.Context, p StdioProtocol, in io.Reader, out io.Writer, opts Options, agents ...rosetta.Agent) error`. ACP serves the first agent. MCP serves all of them.

- [ ] **Step 1: Write the failing tests**

`TestProtocolsMounted` (table per protocol: enabled answers on its path, disabled returns 404). `TestHealthz`: 200, body `ok`. `TestDefaultStore`: nil `Store` still keeps history across two AG-UI runs with one `threadId`. `TestServeStdioACP`: answers `initialize`. `TestServeStdioUnknown`: protocol `"x"` returns an error naming it.

Review Focus 5: `TestNewPanicsOnBadAgents` (table: duplicate names, name `"Bad Name"`; the recovered panic value's message contains the name).

- [ ] **Step 2: Run them and see them fail**

Run: `go test ./server/`
Expected: build failure.

- [ ] **Step 3: Implement the package**

- [ ] **Step 4: Run the tests**

Run: `go test ./server/`
Expected: `ok`.

- [ ] **Step 5: Commit and push**

`git commit -m "feat: add the server package"`

### Task 12: rosetta binary (issues 12 and 13)

**Files:**
- Create: `cmd/rosetta/main.go`, `cmd/rosetta/serve.go`, `cmd/rosetta/stdio.go`, `cmd/rosetta/version.go`
- Test: `cmd/rosetta/main_test.go`, `cmd/rosetta/version_test.go`
- Modify: `AGENTS.md` (commands), `CHANGELOG.md` (Unreleased)

**Interfaces:**
- Consumes: `server.New`, `server.ServeStdio`, `echo.New`, `rosetta.Version`.
- Produces: `func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int`, called by `main` with `os.Args[1:]` and a signal-cancelled context. Commands use the standard `flag` package, one `FlagSet` each.

- [ ] **Step 1: Write the failing tests**

`TestVersion` (table: default prints `rosetta dev go<ver> <os>/<arch>\n`, with `version` set to `v0.1.0` prints that). `TestUsageErrors` (table: no args, unknown command, `stdio` without `--protocol`, `stdio --protocol x`; each returns 2 and writes usage to stderr). `TestStdioACP`: `run(ctx, ["stdio","--protocol","acp"], pipeIn, pipeOut, stderr)` answers `initialize`, stderr holds JSON logs, stdout holds only protocol frames. `TestServe`: `--listen 127.0.0.1:0`, the bound address is logged as `listening`, `/healthz` answers 200, cancelling ctx makes `run` return 0.

- [ ] **Step 2: Run them and see them fail**

Run: `go test ./cmd/rosetta/`
Expected: build failure.

- [ ] **Step 3: Implement the commands**

`serve` flags: `--listen` (default `:8080`), `--agent` (repeatable, default `echo`). `stdio` flags: `--protocol`, `--agent` (repeatable, default `echo`). Graceful shutdown with `http.Server.Shutdown` and a 10 second timeout.

- [ ] **Step 4: Run the tests and the binary**

Run: `go test ./... && go build -o rosetta.exe ./cmd/rosetta && ./rosetta.exe version`
Expected: `ok` everywhere and `rosetta dev go1.27.2 windows/amd64`.

- [ ] **Step 5: Commit and push**

AGENTS.md gets `go run ./cmd/rosetta serve` and `go run ./cmd/rosetta stdio --protocol acp`. `git commit -m "feat: add the rosetta serve, stdio and version commands"`

### Task 13: Protocol docs and the hello example (issues 14 and 15)

**Files:**
- Create: `docs/protocols.md`, `examples/hello/main.go`, `examples/hello/README.md`
- Test: `examples/hello/main_test.go`
- Modify: `README.md` (link to `docs/protocols.md`, quick start with `go run ./cmd/rosetta serve`), `AGENTS.md` (layout: mark built packages), `CHANGELOG.md`

**Interfaces:**
- Consumes: `server.New`, `rosetta.Func`.
- Produces: `func newHandler() http.Handler` in `examples/hello`, used by `main` and the test.

- [ ] **Step 1: Write the failing test**

`TestHelloOverAGUI`: POST a run with user message `"Ada"` to `/agui/hello` on an `httptest.Server` built from `newHandler()`; the body contains `Hello, Ada!`.

- [ ] **Step 2: Run it and see it fail**

Run: `go test ./examples/hello/`
Expected: build failure.

- [ ] **Step 3: Write the example and the docs**

The agent replies `Hello, <text>!`. `docs/protocols.md` has one table per protocol from design section 6, the SDK and spec version each adapter uses (section 3), and "How to add a protocol". `examples/hello/README.md` has one `curl` per HTTP protocol.

- [ ] **Step 4: Run everything**

Run: `go test -cover ./... && golangci-lint run ./...`
Expected: all `ok`, total coverage at or above 80%, `0 issues`.

- [ ] **Step 5: Commit and push, then check CI**

`git commit -m "docs: add the protocol table and the hello example"`, push, and `gh run list --limit 1 --workflow ci` shows `success`. Close issues 1 to 15 that are done, by referencing them in the commit messages (`Closes #N`).
