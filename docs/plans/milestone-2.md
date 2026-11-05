# Milestone 2 Implementation Plan

**Goal:** Config-declared agents that do real work: an `llm` agent that calls models through an OpenAI-compatible endpoint and tools through an MCP server, a `kind: a2a` agent that wraps a remote A2A agent, a YAML config with environment overrides, API key auth, `/readyz` and `/metrics`.

**Architecture:** `llm/openai` is a small streaming chat client. `tools/mcp` lists and calls tools on an MCP server with mcp-go's client. `agents/llm` runs the tool loop over those two through small interfaces, so its tests use fakes. `agents/a2a` wraps `a2aclient`. `config` loads YAML, expands `${VAR}`, applies `ROSETTA_*` overrides and validates. `server` gains API key auth, `/readyz` checks and `/metrics`. `cmd/rosetta` builds agents from config.

**Tech Stack:** Go 1.26, mcp-go v1.5.0 client, a2a-go v2.7.0 `a2aclient`, `go.yaml.in/yaml/v3`, `github.com/prometheus/client_golang`, standard library otherwise.

**Spec:** `docs/design.md` sections 9, 12, 13 and 14.

## Global Constraints

- Everything from milestone 1 still holds: table tests, no network beyond `httptest` and pipes, no `time.Sleep`, 80% total coverage, lint clean, LF golden files, minimal comments, no em-dashes.
- Only `protocol/*`, `tools/mcp` and `agents/a2a` import a protocol SDK.
- Outbound calls to transponder and airlock carry the caller's `Authorization` unchanged, or `Bearer <api_key>` from config when there is no caller token, plus `traceparent`, `tracestate`, `X-Request-ID` (kept or generated) and `X-Project`.
- The remote A2A agent forwards the caller's token only with `forward_token: true`.
- Environment overrides use the `ROSETTA_` prefix and `__` between nested keys.
- Roles are `admin`, `member` and `viewer`. Only `admin` and `member` may run agents.
- Work happens on branch `milestone-2`, pushed after every commit, merged into `main` through a pull request once CI is green.

## Review Focus

1. **Upstream down.** transponder or airlock refuses the connection or answers 500. Expected: the run fails with a message naming the upstream (`transponder: ...`, `airlock: ...`), not a raw dial error with no context. Tests in Tasks 2, 3 and 4.
2. **Malformed stream.** The model's SSE stream has a broken JSON line, ends without `[DONE]`, or splits one tool call's arguments over many chunks. Expected: split arguments are joined by tool call index, broken JSON fails the run with an error, a stream that ends early returns what arrived. Tests in Task 2.
3. **Tool loop that never ends.** The model keeps asking for tools. Expected: the run stops after `max_steps` model calls with an error saying the step limit was hit. Test in Task 4.
4. **Secrets in config.** `${VAR}` refers to an unset variable, or an API key is empty. Expected: an unset variable expands to empty and validation then names the field. An empty key is rejected. Tests in Task 6.
5. **Wrong or missing key.** No `Authorization`, a wrong key, a `viewer` key, and a timing-safe compare. Expected: 401 with `WWW-Authenticate: Bearer` for missing or wrong, 403 for viewer, health and agent cards stay public. Tests in Task 8.

---

### Task 1: Caller headers in and out

**Files:** Modify `rosetta.go`, `internal/httpx/httpx.go`, `protocol/agui/agui.go`, `protocol/mcp/mcp.go`, `protocol/a2a/executor.go`. Test `internal/httpx/httpx_test.go`.

**Interfaces:**
- Produces: `rosetta.Caller` gains `Project string`. `func httpx.Caller(get func(string) string) rosetta.Caller` reads `Authorization` (Bearer only), `X-Request-ID` (generated as 32 hex chars when empty), `traceparent` and `tracestate` into `Trace`, and `X-Project`. `func httpx.Outbound(c rosetta.Caller, fallbackKey string) http.Header` builds the upstream headers per the Global Constraints, leaving out empty ones.

- [ ] Write `TestCaller` (table: full headers, lowercase scheme, Basic scheme gives no token, missing request ID is generated and 32 long) and `TestOutbound` (caller token wins over fallback, fallback used without a token, no token and no fallback gives no `Authorization`, trace and project copied). Run `go test ./internal/httpx`, expect build failure.
- [ ] Implement, switch the three adapters to `httpx.Caller`, run `go test ./...`, expect `ok`.
- [ ] Commit `feat: read and forward caller headers`.

### Task 2: OpenAI-compatible streaming client

**Files:** Create `llm/openai/openai.go`, `llm/openai/stream.go`. Test `llm/openai/openai_test.go`.

**Interfaces:**
- Produces:
  - `type Message struct { Role, Content, ToolCallID string; ToolCalls []ToolCall }` with JSON tags `role`, `content`, `tool_call_id,omitempty`, `tool_calls,omitempty`.
  - `type ToolCall struct { ID, Name, Arguments string }` encoded as `{"id","type":"function","function":{"name","arguments"}}`.
  - `type Tool struct { Name, Description string; Parameters json.RawMessage }` encoded as `{"type":"function","function":{...}}`.
  - `type Request struct { Model string; Messages []Message; Tools []Tool }`.
  - `type Response struct { Message Message; InputTokens, OutputTokens int }`.
  - `type Client struct { URL, APIKey string; HTTP *http.Client }`.
  - `func (c *Client) Stream(ctx context.Context, req Request, h http.Header, onText func(string) error) (Response, error)`. Posts to `URL + "/chat/completions"` with `stream: true` and `stream_options.include_usage`. `h` comes from `httpx.Outbound` and the client adds `Bearer APIKey` only when `h` has no `Authorization`.
- Usage: trailers `X-Usage-Input-Tokens` and `X-Usage-Output-Tokens` win, the stream's `usage` chunk is the fallback.
- Errors start with `transponder: `, include the status and the upstream `error.message` when the body has one.

- [ ] Write tests against an `httptest` server: `TestStreamText` (three deltas, onText called in order, message content joined), `TestStreamToolCalls` (two tool calls, arguments split over chunks by index), `TestStreamUsage` (table: trailers only, usage chunk only, both with trailers winning), `TestStreamHeaders` (caller header passes, fallback key used only without one, body has `stream: true`), `TestStreamErrors` (table: 500 with JSON error, 401, broken JSON line, connection refused via a closed server, onText error stops the stream). Run, expect build failure.
- [ ] Implement with `bufio.Scanner` over `data:` lines, stop at `[DONE]`, read `resp.Trailer` after the body is drained. Run `go test ./llm/...`, expect `ok`.
- [ ] Commit `feat: add the OpenAI-compatible streaming client`.

### Task 3: MCP tool source

**Files:** Create `tools/mcp/mcp.go`. Test `tools/mcp/mcp_test.go`.

**Interfaces:**
- Produces:
  - `type Tool struct { Name, Description string; Schema json.RawMessage }`.
  - `type Source struct { URL, APIKey string; HTTP *http.Client }`.
  - `func (s *Source) Open(ctx context.Context, h http.Header) (*Session, error)` connects with mcp-go's Streamable HTTP client using `h` (plus `Bearer APIKey` when `h` has no `Authorization`) and initializes. One session per run, so each run talks to airlock as its own caller.
  - `func (s *Session) List(ctx context.Context, globs []string) ([]Tool, error)`: all pages, filtered with `path.Match` against each glob. Empty globs means no tools.
  - `func (s *Session) Call(ctx context.Context, name string, args json.RawMessage) (text string, isError bool, err error)`: text content joined with newlines, structured content as JSON when there is no text.
  - `func (s *Session) Close() error`.
- Errors start with `airlock: `.

- [ ] Write tests against an in-process mcp-go Streamable HTTP server on `httptest`: `TestList` (table of globs: `*`, `github__*`, exact name, none), `TestCall` (text result, error result, structured only), `TestHeaders` (server sees forwarded `Authorization` and `X-Project`), `TestUnreachable` (closed server, error starts with `airlock:`). Run, expect build failure.
- [ ] Implement, run `go test ./tools/...`, expect `ok`.
- [ ] Commit `feat: add the MCP tool source`.

### Task 4: The llm agent

**Files:** Create `agents/llm/llm.go`. Test `agents/llm/llm_test.go`.

**Interfaces:**
- Consumes: Tasks 1 to 3.
- Produces:
  - `type Chat interface { Stream(ctx context.Context, req openai.Request, h http.Header, onText func(string) error) (openai.Response, error) }`.
  - `type Tools interface { Open(ctx context.Context, h http.Header) (ToolSession, error) }` and `type ToolSession interface { List(ctx, globs []string) ([]mcp.Tool, error); Call(ctx, name string, args json.RawMessage) (string, bool, error); Close() error }`, with small adapters so `*openai.Client` and `*mcp.Source` satisfy them.
  - `type Config struct { Name, Description, Model, SystemPrompt string; Tools []string; MaxSteps int }`. `MaxSteps` 0 means 8.
  - `func New(cfg Config, chat Chat, tools Tools) rosetta.Agent`. `tools` may be nil: the agent then runs without tools.
- Loop per design section 9: system prompt, history (user to `user`, agent to `assistant`, system to `system`, data parts as their JSON text), input; list tools once per run when `cfg.Tools` is not empty; stream text to `out.Text`; for each tool call `out.ToolCall`, call, `out.ToolResult` with `{"text": ...}`, append a `tool` message; send a `rosetta.Usage` per model call; stop when there are no tool calls; after `MaxSteps` model calls return `llm: step limit of N reached`.
- Outbound headers come from `httpx.Outbound(req.Caller, "")`, leaving the fallback key to each client.

- [ ] Write tests with a scripted fake `Chat` and fake `Tools`: `TestPlainAnswer`, `TestToolLoop` (one tool round then an answer, events in order: ToolCall, ToolResult, TextDelta, Usage twice), `TestMessagesBuilt` (system prompt, history roles, input as last user message), `TestStepLimit` (MaxSteps 2, model always calls a tool), `TestNoToolsConfigured` (Tools never opened), `TestToolErrorResult` (isError passes through), `TestUpstreamError` (chat error fails the run), `TestHeadersForwarded`. One end-to-end test wires the real `openai.Client` and `mcp.Source` to `httptest` servers. Run, expect build failure.
- [ ] Implement, run `go test ./agents/...`, expect `ok`.
- [ ] Commit `feat: add the llm agent`.

### Task 5: The remote A2A agent

**Files:** Create `agents/a2a/a2a.go`. Test `agents/a2a/a2a_test.go`.

**Interfaces:**
- Produces: `type Config struct { Name, Description, URL string; ForwardToken bool; HTTP *http.Client }` and `func New(ctx context.Context, cfg Config) rosetta.Agent`.
- `New` tries to resolve the remote card once, with the context's deadline. When that works, `Card()` takes description and skills from it (config description wins when set). When it fails, the agent still starts, and the first run resolves it again.
- `Run` sends the input as a user message with `a2aclient` (streaming when the card says so), passing the session ID as the A2A context ID. Mapping: artifact text parts to `TextDelta`, artifact data parts to `Artifact`, `working` status messages to `Step`, `failed` or `rejected` to an error with the status message text, final message text to `TextDelta`. Headers per the Global Constraints, with `Authorization` only when `ForwardToken`.

- [ ] Write tests against a real rosetta A2A adapter on `httptest` (`protocol/a2a` serving a scripted agent), so the bridge is checked against the same wire code: `TestCardFromRemote`, `TestStreamRun` (text and artifact come back), `TestFailedRemote` (remote agent errors, run fails with its message), `TestUnreachableAtStart` (agent starts, first run errors with `a2a:`), `TestForwardToken` (table: false drops it, true passes it). Run, expect build failure.
- [ ] Implement, run `go test ./agents/...`, expect `ok`.
- [ ] Commit `feat: add the remote A2A agent kind`.

### Task 6: Config

**Files:** Create `config/config.go`, `config/env.go`. Test `config/config_test.go`, `config/testdata/full.yaml`.

**Interfaces:**
- Produces: `type Config` mirroring design section 9 (`ListenAddr`, `DefaultAgent`, `Storage{DSN, SecretKey}`, `Auth{Disabled bool; APIKeys []APIKey{Name, Key, Role}}`, `Transponder{URL, APIKey}`, `Airlock{URL, APIKey}`, `Protocols{MCP, A2A, AGUI Protocol{Enabled *bool; Path string}}`, `Sessions{TTL time.Duration}`, `Agents []Agent{Name, Description, Kind, Model, SystemPrompt string; Tools []string; MaxSteps int; URL string; ForwardToken bool}`), `func Load(path string, environ []string) (*Config, error)` and `func Parse(data []byte, environ []string) (*Config, error)`.
- Order: expand `${NAME}` from `environ` (only that form, so `$5` in a prompt stays), decode YAML into a map, apply `ROSETTA_*` overrides (key parts lowercased, numeric parts index lists, values parsed as YAML scalars), decode into `Config` with unknown keys rejected, apply defaults (`:8080`, protocols enabled, TTL 24h), validate.
- Validation names the field: agent name format and uniqueness, kind is `llm`, `a2a` or `echo`, `llm` needs `model` and a `transponder.url`, `a2a` needs `url`, roles, empty keys, `default_agent` exists, auth on with no keys.

- [ ] Write `TestParseFull` (testdata file, every field), `TestDefaults`, `TestEnvOverride` (table: `ROSETTA_LISTEN_ADDR`, `ROSETTA_TRANSPONDER__URL`, `ROSETTA_AUTH__DISABLED=true` as a bool, `ROSETTA_AGENTS__0__MODEL`), `TestExpand` (set, unset, `$5` untouched), `TestValidate` (table, one case per rule, checking the field name in the error), `TestUnknownKey`. Run, expect build failure.
- [ ] Implement, run `go test ./config`, expect `ok`.
- [ ] Commit `feat: add YAML config with environment overrides`.

### Task 7: Metrics and readiness

**Files:** Modify `internal/run/run.go`, `server/server.go`. Create `internal/metrics/metrics.go`, `server/ready.go`. Test `internal/run/run_test.go`, `internal/metrics/metrics_test.go`, `server/ready_test.go`.

**Interfaces:**
- Produces:
  - `run.Observer` interface `{ RunDone(agent string, err error, d time.Duration, u rosetta.Usage); StoreError(op string) }` and `run.WithObserver(Observer)`. The runner sums `Usage` events of a run.
  - `metrics.New() *Metrics` implementing `run.Observer` on its own registry, `(*Metrics).Handler() http.Handler`. Series: `rosetta_runs_total{agent,status}` (`completed`, `failed`, `cancelled`), `rosetta_run_duration_seconds{agent}`, `rosetta_tokens_total{agent,direction}` (`input`, `output`), `rosetta_store_errors_total{op}`.
  - `server.Check struct { Name string; Check func(context.Context) error }`, `server.Options.Checks []Check`, `func server.HTTPCheck(name, url string, client *http.Client) Check` (any HTTP answer below 500 is reachable).
  - `GET /readyz` runs checks with a 2 second timeout each and answers `{"status":"ok"|"unavailable","checks":{"<name>":"ok"|"<error>"}}`, 200 or 503. `GET /metrics` on every server.

- [ ] Write `TestObserver` in run (completed, failed, cancelled, usage summed, store error counted), `TestMetrics` (scrape the handler, series present with labels), `TestReadyz` (table: no checks, all ok, one failing gives 503 and names it), `TestHTTPCheck` (200, 404 ok, 500 fails, closed server fails). Run, expect build failure.
- [ ] Implement, run `go test ./...`, expect `ok`.
- [ ] Commit `feat: add /readyz and /metrics`.

### Task 8: API key auth

**Files:** Create `internal/auth/auth.go`. Modify `server/server.go`. Test `internal/auth/auth_test.go`, `server/server_test.go`.

**Interfaces:**
- Produces: `server.APIKey struct { Name, Key, Role string }` and `server.Options.APIKeys []APIKey`. Empty means no auth, so library users opt in. `auth.Middleware(keys, next) http.Handler`.
- Keys are compared as SHA-256 digests with `subtle.ConstantTimeCompare`. Missing or wrong key: 401, `WWW-Authenticate: Bearer`, JSON error `{"error":{"code":"unauthorized","message":...}}`. Viewer: 403 `forbidden`. Public: `/healthz`, `/readyz`, `/metrics` and any path ending in `/.well-known/agent-card.json`.

- [ ] Write `TestMiddleware` (table: none, wrong, Basic scheme, admin, member, viewer, public paths without a key) and a server test where MCP, A2A and AG-UI each reject a missing key and accept a member key. Run, expect build failure.
- [ ] Implement, run `go test ./...`, expect `ok`.
- [ ] Commit `feat: add API key auth for protocol endpoints`.

### Task 9: Wire config into the binary

**Files:** Modify `cmd/rosetta/main.go`, `serve.go`, `stdio.go`. Create `cmd/rosetta/agents.go`, `examples/rosetta.yaml`. Test `cmd/rosetta/main_test.go`, `cmd/rosetta/agents_test.go`. Docs: README, AGENTS.md, CHANGELOG, design status line.

**Interfaces:**
- Consumes: Tasks 2 to 8.
- `--config path` on `serve` and `stdio`. With a config: agents, listen address (`--listen` still wins), default agent, protocol toggles, session TTL, keys, readiness checks for each configured upstream. `stdio --agent name` picks agents from the config. Without a config: the echo agents from milestone 1, auth off, and a warning log line saying so.
- `func buildAgents(ctx context.Context, cfg *config.Config) ([]rosetta.Agent, error)`.

- [ ] Write `TestBuildAgents` (each kind, unknown agent name for stdio), `TestServeConfig` (serve with a temp config pointing at a fake OpenAI server: AG-UI run streams the model's text, a request without a key gets 401, `/readyz` 200, `/metrics` has a `rosetta_runs_total` line), `TestServeNoConfigWarns`, `TestStdioConfigAgent`. Run, expect failure.
- [ ] Implement, run `go test ./...` and `golangci-lint run ./...`, expect both clean, coverage at or above 80%.
- [ ] Update docs, commit `feat: serve config-declared agents from the binary`, push, open the pull request, wait for CI, merge.
