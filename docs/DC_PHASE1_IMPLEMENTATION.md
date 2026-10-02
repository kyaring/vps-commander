# DC Refactoring Phase 1 Implementation Record

> 文档属性：历史实施记录。后续阶段和当前状态以当前架构规范与 v2.1 规范为准。

Date: 2026-10-02

## Completed

- Added immutable Hub `PolicySnapshot` protected by `RWMutex`.
- Added monotonic security policy version persisted in SQLite.
- Hub startup now rebuilds the snapshot from SQLite; snapshot load failure aborts startup rather than defaulting to High.
- Security setting writes use SQLite transactions and only publish the new snapshot after the durable write succeeds.
- Added a compile-time Tool Registry with fixed `RequiredRisk` for Low/Medium/High capabilities.
- Existing API security checks now resolve required risk through the registry instead of duplicating risk numbers in handlers.
- Security-setting audit records include old/new effective mode and snapshot version.

## Compatibility / Safety

- Existing API routes and Agent WebSocket protocol were not changed in this phase.
- No production Hub/Agent restart was performed.
- When `Server.Policy` is initialized, security checks use the in-memory snapshot and do not query SQLite on the request hot path.
- Existing tests that construct `Server` without a snapshot retain the legacy storage fallback for test/backward compatibility; production startup always initializes the snapshot.

## Verification

- `go test ./...` passed.
- `go test -race ./...` passed.
- Hub and Agent clean builds passed.
- No production service restart was performed; live rollout remains a separate controlled step to preserve the no-interruption requirement.

## Phase 2 Completed

- Added `edit_block` to the Agent execution engine.
- `old_text` must match exactly once; zero/multiple matches are rejected without mutation.
- Replacement uses a same-directory temporary file, preserves file permission bits, fsyncs the file, renames atomically, and syncs the directory.
- Added Agent RPC action `edit_block`, Hub `/api/v1/devices/file/edit_block`, MCP `edit_block`, and stdio proxy support.
- `edit_block` is registered as Medium risk and therefore follows the existing Hub policy snapshot gate.
- Added optional `sub_action` protocol field without changing existing message semantics.
- Added tests for unique replacement, duplicate/missing rejection, permissions, Low-mode denial, and MCP registration.

Phase 2 rollout remains deliberately separate from production process replacement. Existing production Agent connections have not been restarted.

## Phase 3 Completed

- Added a per-Agent `SessionManager` with a bounded session count (default 16).
- Added fixed-size 2 MiB stdout/stderr RingBuffers; old output is discarded when capacity is exceeded.
- Added long-running process actions: `start_process`, `read_process_output`, `interact_with_process`, `force_terminate`, `list_sessions`.
- Processes run in their own process group so termination/force termination covers the command tree.
- Added periodic completed-session cleanup.
- Added Hub RPC and HTTP `/api/v1/process/session` dispatch with action-based lifecycle operations.
- Added session tests for ring retention, output capture, termination, duplicate IDs, and session limits.
- Existing WebSocket request/response protocol remains compatible; new process actions are additive.

Production Hub/Agents were not restarted during Phase 3 implementation.

## Phase 3 Security Hardening / Concurrency Gate

- Added per-node operation arbitration in `cluster.Node.OpMu`.
- Low-risk read actions use `RLock` and may run concurrently.
- Medium/High mutations use exclusive `Lock`, preventing overlapping writes/control operations against the same node.
- Unknown actions fail closed as High risk for the concurrency gate.
- Session IDs are generated with 128-bit `crypto/rand` entropy when omitted by the API caller; PID is never used as identity.
- Session ownership is bound to a non-reversible SHA-256 fingerprint of the authenticated API credential and checked by Agent for read/input/terminate operations.
- Session TTL is enforced; stdin is capped at 64 KiB per call.
- Agent restart still leaves all sessions in process memory only, so old session state is not restored.

All validation passed with `go test ./...`, `go test -race ./...`, Hub/Agent builds, and `git diff --check`. No production process was restarted.

## Phase 4 Completed

- Added optional protocol/agent capability metadata: `ProtocolVersion`, `AgentVersion`, `Capabilities`.
- Hub records the capability set per connected Agent; when a modern Agent explicitly advertises capabilities, unsupported actions fail with `capability missing` rather than silently degrading.
- Old Agents remain compatible because capability metadata is optional and capability enforcement is only applied when a capability set is advertised.
- Added structured read-only filesystem operations: `list_directory`, `get_file_info`, `start_search`.
- Directory listing supports bounded recursive traversal and a maximum entry count.
- File metadata exposes type, size, mode and modification time.
- Search is context-cancellable and bounded by a result cap; symlinks are skipped to avoid traversal surprises.
- Added Hub endpoints `/api/v1/file/list`, `/api/v1/file/info`, `/api/v1/file/search`.
- Added executor tests for directory listing, file metadata and bounded search.

Phase 4 validation: full tests, race tests, Hub/Agent builds and `git diff --check` passed. Production Hub/Agents were not restarted.
