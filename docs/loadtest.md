# VPS-Commander B7 Load Test

`cmd/loadtest` simulates Agent WebSocket clients and MCP clients against an explicitly selected Hub. It does not restart the Hub. Fake Agents use unique names (`<prefix>-<run-id>-NNNN`) and answer Hub `ping` messages plus `exec` requests with a synthetic `loadtest-ok` response; no shell command is executed by the fake Agent.

## Recommended 500 + 50 run

Run this from a separate test machine against a **test Hub**, not production. The `-agent-token` defaults to `VPS_COMMANDER_CLUSTER_SECRET`; for generated, unregistered fake device names, this must match the Hub's cluster secret. MCP clients should use tokens with `exec` scope, and the Hub security policy must allow the low-risk `echo loadtest` command.

Create `loadtest-tokens.txt` with 50 API tokens, one per line (no names or extra columns), each with `exec` scope and at least one allowed MCP session. Protect it with `chmod 600 loadtest-tokens.txt`. One shared token is usually insufficient for 50 clients because its `max_sessions` and `rate_rps` limits are enforced intentionally.

```bash
export VPS_COMMANDER_CLUSTER_SECRET='the-test-hub-cluster-secret'
export VPS_COMMANDER_LOADTEST_METRICS_TOKEN='an-admin-scope-token'
go run ./cmd/loadtest \
  -hub https://test-hub.example \
  -mcp-token-file ./loadtest-tokens.txt \
  -agents 500 \
  -mcp-clients 50 \
  -agent-ramp 90s \
  -startup-timeout 2m \
  -mcp-ramp 10s \
  -interval 30s \
  -duration 10m \
  -strict
```

By default, 25 MCP clients use legacy SSE and 25 use Streamable HTTP. Each runs `initialize`, `tools/list`, and repeated `tools/call` requests. The default tool is `exec_command` with the fixed command `echo loadtest`, targeting only generated fake Agent names. Use `-tool list_devices` for a read-only MCP-only check.

The tool writes progress to stderr and a final JSON summary to stdout. It includes Agent connection attempts/retries/429s, fake Agent exec responses, active MCP sessions by transport, tool-call success/errors/429s, and a best-effort `/metrics` scrape. `-metrics-token` can override the metrics token; otherwise the first MCP token is tried. A metrics scrape failure (for example, missing `admin` scope) is reported in the JSON and does not hide the load-test results.

`-strict` checks that all requested Agents and MCP sessions are still connected at the end and that MCP tool calls made progress. It intentionally does not fail solely because a 429 occurred: B2/B4 rate limits are part of the test and their counts remain in the summary. Evaluate the B-series acceptance thresholds from the final summary and Hub metrics after the run. The tool itself does not declare B1–B7 acceptance passed.

## Useful flags

- `-hub`: required Hub base URL (`http://` or `https://`).
- `-agents`, `-mcp-clients`: simulated population; defaults 500 and 50.
- `-duration`: steady-state duration; default 10 minutes after the Agent startup phase.
- `-agent-ramp`, `-mcp-ramp`: spread connection startup and avoid creating an artificial synchronized burst.
- `-startup-timeout`: wait for all Agents before starting MCP clients; default 2 minutes.
- `-interval`: delay between MCP `tools/list` and `tools/call` cycles; default 30 seconds.
- `-mcp-token-file`: newline-separated token list, assigned round-robin.
- `-metrics-token`: optional admin-scope token for `/metrics`.
- `-strict`: exit nonzero if target populations/sessions are not present at the end or no MCP tool call succeeds.
