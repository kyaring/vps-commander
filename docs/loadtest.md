# VPS-Commander B7 Load Test

`cmd/loadtest` simulates Agent WebSocket clients and MCP clients against an explicitly selected Hub. It does not restart the Hub. Fake Agents use unique names (`<prefix>-<run-id>-NNNN`) and answer Hub `ping` messages plus `exec` requests with a synthetic `loadtest-ok` response; no shell command is executed by the fake Agent.

## Recommended 500 + 50 run

**Never run this tool against production.** It registers synthetic device names and persists device/profile/audit state. Run it from a separate test machine against a disposable **test Hub** and a copy of the database. The `-agent-token` defaults to `VPS_COMMANDER_CLUSTER_SECRET`; for generated, unregistered fake device names, this must match the Hub's cluster secret. MCP clients should use tokens with `exec` scope.

Before an `exec_command` run, log in to the **test Hub** panel as an administrator, open the **安全** card, set **全局安全等级** to **高 · High**, and click **保存全局策略**. Confirm the displayed effective policy is `high`. The default `medium` policy denies `exec`; the load tester detects `security mode … denies exec`, prints a preflight failure and exits nonzero instead of wasting the whole run. Do not weaken production policy for a load test. Restore the test Hub's previous policy after the run if needed.

Create `loadtest-tokens.txt` with 50 API tokens, one per line (no names or extra columns), each with `exec` scope and at least one allowed MCP session. Protect it with `chmod 600 loadtest-tokens.txt`. One shared token is usually insufficient for 50 clients because its `max_sessions` and `rate_rps` limits are enforced intentionally.

```bash
export VPS_COMMANDER_CLUSTER_SECRET='the-test-hub-cluster-secret'
export VPS_COMMANDER_LOADTEST_METRICS_TOKEN='an-admin-scope-token'
go run ./cmd/loadtest \
  -hub https://test-hub.example \
  -mcp-token-file ./loadtest-tokens.txt \
  -agents 500 \
  -mcp-clients 50 \
  -agent-ramp 110s \
  -startup-timeout 2m \
  -mcp-ramp 10s \
  -interval 30s \
  -duration 10m \
  -strict
```

By default, 25 MCP clients use legacy SSE and 25 use Streamable HTTP. Each runs `initialize`, `tools/list`, and repeated `tools/call` requests. The default tool is `exec_command` with the fixed command `echo loadtest`, targeting only generated fake Agent names. Use `-tool list_devices` for a read-only MCP-only check.

The tool writes progress to stderr and a final JSON summary to stdout. It includes a unique `run_id` and `agent_name_prefix` for cleanup, Agent connection attempts/retries/429s, fake Agent exec responses, active MCP sessions by transport, tool-call success/errors/429s, periodic live `/metrics` samples, and `/api/v1/devices` latency percentiles. Metrics and device-latency sampling require an admin-scope token. `-metrics-token` can override the metrics token; otherwise the first MCP token is tried. A metrics scrape failure (for example, missing `admin` scope) is reported in the JSON and does not hide the load-test results.

`-strict` checks that all requested Agents and MCP sessions are still connected at the end, at least one tool call was attempted and succeeded, and no exec security denial was detected. It intentionally does not fail solely because a 429 occurred: B2/B4 rate limits are part of the test and their counts remain in the summary. Evaluate the B-series acceptance thresholds from the final summary and Hub metrics after the run. The tool itself does not declare B1–B7 acceptance passed.

## Useful flags

- `-hub`: required Hub base URL (`http://` or `https://`).
- `-agents`, `-mcp-clients`: simulated population; defaults 500 and 50.
- `-duration`: steady-state duration; default 10 minutes after the Agent startup phase.
- `-agent-ramp`, `-mcp-ramp`: spread connection startup and avoid creating an artificial synchronized burst. The Hub limits Agent WebSocket handshakes per source IP to 5/s with burst 20. From one source IP, set `-agent-ramp` to at least `agents/5` seconds (500 Agents: at least 100s; recommended 110s). A shorter ramp causes 429/retry churn; alternatively distribute the Agent clients across multiple test machines/IPs.
- `-startup-timeout`: wait for all Agents before starting MCP clients; default 2 minutes.
- `-interval`: delay between MCP `tools/list` and `tools/call` cycles; default 30 seconds.
- `-mcp-token-file`: newline-separated token list, assigned round-robin.
- `-sample-interval`: periodic sampling interval for `/metrics` and `/api/v1/devices` latency; default 5 seconds.
- `-metrics-token`: admin-scope token used for `/metrics` and devices latency sampling.
- `-strict`: exit nonzero if target populations/sessions are not present at the end, no tool call succeeds, or exec is denied by policy.


## Remove test data after the run

Stop the load tester first. The summary's `agent_name_prefix` is the exact prefix for this run, for example `loadtest-20261009T165500-a1b2`. On the **test database only**, substitute that exact prefix (do not use a broad wildcard such as `loadtest-%` if other test runs must be retained):

```sql
BEGIN;
DELETE FROM audit_logs WHERE device LIKE 'loadtest-20261009T165500-a1b2-%';
DELETE FROM device_credentials WHERE name LIKE 'loadtest-20261009T165500-a1b2-%';
DELETE FROM revoked_devices WHERE name LIKE 'loadtest-20261009T165500-a1b2-%';
DELETE FROM devices WHERE name LIKE 'loadtest-20261009T165500-a1b2-%';
COMMIT;
```

The example prefix is illustrative; use the exact `agent_name_prefix` emitted by your run. Back up the disposable test database before cleanup. Do not execute these statements on production.
