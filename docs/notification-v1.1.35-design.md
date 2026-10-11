# Notification system v1.1.35 design

## Confirmed channel choices and platform constraint

- Telegram Bot is selected for proactive operational notifications.
- QQ official bot is selected instead of OneBot. The official QQ bot documentation currently states that proactive push has not been provided since 2025-04-21. Passive replies require an inbound message and recall is subject to platform eligibility rules. Therefore the QQ adapter must advertise its actual capabilities and must not be treated as a reliable asynchronous alert destination unless the platform restores/authorizes that capability. A credential check is not proof that proactive delivery works.
- Webhook remains supported and must retain its current payload/behavior through migration.

Official references:
- https://github.com/tencent-connect/bot-docs/blob/main/docs/develop/api-v2/server-inter/message/send-receive/send.md
- https://github.com/tencent-connect/bot-docs/blob/main/docs/README.md
- https://core.telegram.org/bots/api#sendmessage

## Three-layer model

1. **Channel**: a concrete destination and its provider configuration. `type` describes the adapter (`webhook`, `telegram_bot`, `qq_official_bot`); a channel row is one configured instance. Secrets and endpoint URLs must never be echoed by list/read APIs. Provider capability (proactive vs passive-only) is adapter metadata, not an operator-editable claim.
2. **Event template**: maps an event type and severity to a safe message format. Event severity is an immutable property of the event (`P1`–`P4`), not a task setting. Templates use allowlisted variables and must never evaluate arbitrary expressions or recursively render caller-supplied template text.
3. **Task**: routes a matching event/template to a channel. Task filters may select event types and severity ranges; policy owns per-event repeat intervals, maximum reminders, recovery behavior/cooldown, quiet windows and merge windows. A task may target one channel/template pair; multiple destinations are represented by multiple tasks so each delivery can be independently audited.

Durable runtime entities:
- `notification_events`: accepted normalized events, source, severity, payload, actor and optional token-scoped idempotency key.
- `notification_deliveries`: one durable delivery record per event/task, with attempt state and next retry time.
- `notification_task_state`: per-task event episode counters and dedupe/quiet/merge state.

## External event API security

- `POST /api/v1/events` requires the dedicated `events:emit` scope; it does not imply `admin`, channel management or task/template management.
- Event severity defaults to the selected template. External callers may lower severity, but P1 or any severity escalation requires an `admin` scope and is audited.
- Require a bounded JSON body (32 KiB initially), validate event type against registered templates, allowlist variable names/types and cap string lengths. Treat all values as data; escape according to the output format.
- Apply a per-token fixed-window quota of 30 requests/minute and return `429` with `Retry-After`; audit accepted, rejected, rate-limited and idempotent-replay outcomes without logging credentials or full sensitive payloads.
- `Idempotency-Key` is optional; when present it is unique per token. A repeated key with the same request returns the original event result; reusing the key with a different request returns `409`.
- A CLI wrapper is available at `scripts/emit-notification-event.py`. It requires `VPS_COMMANDER_HUB_URL`, `VPS_COMMANDER_EVENT_TOKEN` (a token with `events:emit`), and a stable `--idempotency-key`. Example after a development task has a final commit:

  ```sh
  export VPS_COMMANDER_HUB_URL='https://hub.example.invalid'
  export VPS_COMMANDER_EVENT_TOKEN='(read from the one-time token display; do not store in the repository)'
  python3 scripts/emit-notification-event.py development-completed \
    --variables '{"task":"notification outbox","commit":"abc1234","summary":"tests and build passed"}' \
    --idempotency-key 'development:notification-outbox:abc1234'
  ```

  After review, emit `review-completed` with `result` set to `PASS` or `FAIL`, plus `commit` and `summary`. The script does not print the token. `scripts/notify-agent-completion.sh` is the explicit final-boundary helper: it skips successfully without credentials and treats delivery failures as non-blocking. It is not an automatic external agent hook, and this change does not provision a token or deploy it.

## Compatibility migration

- Keep the old `webhook_targets` and `webhook_delivery_state` tables during the transition.
- The initial migration backfills each legacy webhook to a namespaced channel (`legacy-webhook:<id>`) and default task (`legacy-task:<id>`) using the raw-event JSON template. It also carries over last delivery status.
- Legacy create/update/delete and delivery-state writes update the shadow channel/task in the same SQLite transaction. Deletion disables shadow rows instead of removing them, preserving future delivery history.
- Built-in offline/resource/report notifications continue using the old sender during this phase. External custom events already use the durable outbox and new Webhook/Telegram adapters. Do not cut over built-in events until parity tests pass.

## Staged implementation

1. Additive schema, idempotent legacy backfill, transactional shadow writes, and storage tests. No behavior change.
2. Add channel/template/task management APIs and safe response DTOs; add `events:emit` to token scope validation and tests.
3. Implement normalized event acceptance, idempotency, per-token rate limit and audit; route to durable delivery rows.
4. Add provider adapters: Telegram proactive sends; QQ official bot is capability-limited/passive-only unless a supported proactive mode is confirmed. Add test-send/status reporting and credential non-echo tests.
5. Migrate built-in offline/resource/report notifications to templates/tasks while comparing output with the old sender; switch routing only after parity tests.
6. Add UI for channels, templates and tasks; add development-complete and review-complete emitters after the event API is stable.

## Current implementation status (not a release)

- Completed locally: additive schema/backfill, channel/template/task admin APIs, encrypted provider credentials, `events:emit` scope, `POST /api/v1/events`, per-token rate limit, idempotency, event/task routing, durable delivery rows, Webhook/Telegram adapters and retry scheduling.
- Completed locally in this slice: channel/template/task management UI, channel test-send API and last-attempt/success/failure status, delivery history with per-attempt outcomes, and single-delivery manual retry.
- Implemented locally in this slice: validated task policy schema, repeat interval/max-reminder suppression, timezone-aware quiet windows, pending-delivery merge windows, recovery episode closure/cooldown, built-in event templates, and legacy/shadow/v1 routing modes. Targeted policy and mode tests pass.
- Completed locally: legacy/shadow/v1 parity fixtures cover exactly-one legacy send in shadow mode, zero shadow outbox attempts, durable v1 delivery, legacy payload compatibility (including severity/data), and customized built-in template rendering without changing legacy payloads. Full regression is green.
- Intentionally not performed: production shadow observation, changing the Hub environment, restarting the production Hub, or switching live traffic. These require an operator-approved window; no push, merge, tag, release or deployment has occurred.
- Delivery semantics are at-least-once. If a provider accepts a message but the response is lost, retry may produce a duplicate; new structured Webhook payloads include the stable event ID for receiver-side deduplication.
- QQ official bot remains disabled as an asynchronous alert destination until the intended target type and current platform capability are confirmed. The official docs state proactive push has been withdrawn since 2025-04-21 for the relevant user/group scenarios; do not treat QQ credentials as proof of send capability.

## Task policy semantics and built-in notification migration

Task `policy_json` accepts only the documented fields below; unknown fields are rejected:

- `repeat_interval_seconds` (0 disables interval suppression), `max_reminders` (0 means unlimited). The counter records notifications queued by policy, not confirmed provider deliveries. `max_reminders` is a per-incident-episode cap: a matching configured recovery event closes the episode and the next alert resets the count to 0 before it is evaluated. Time passing alone does **not** reset the cap; if no matching recovery event arrives, the same active episode stays capped. Configure recovery event mappings if you want a new episode to receive a fresh budget.
- `quiet_start` + `quiet_end` are both required when configured, in local `HH:MM`; `timezone` is an IANA name and defaults to UTC. The start is inclusive and the end exclusive; overnight windows are supported.
- `merge_window_seconds` (0 disables merging). The first pending delivery opens a window; later events for the same task/event/device/source inside that window append to the delivery snapshot. Original event rows stay immutable; the extra per-event delivery rows are marked `suppressed` with a reference to the aggregate delivery.
- `recovery_event_types` identifies recovery event types handled by this task; `recovery_for_event_types` explicitly lists the alert event types they resolve. Recovery closes matching active episodes for the same channel and device/source, not unrelated alerts. `notify_recovery` defaults to true; `recovery_cooldown_seconds` suppresses repeated recovery notices while still closing the episode.

Built-in `node_offline`, `node_recovery`, `resource_threshold`, `resource_recovery`, and `scheduled_report` events have seeded templates. `VPS_COMMANDER_NOTIFICATION_V1_BUILTIN_MODE` is read by the running Hub for each event; set it in the Hub environment before startup:

- `legacy` (default): existing sender remains the only sender.
- `shadow`: persist normalized events and shadow delivery rows, while the existing sender continues to deliver. Shadow rows are never claimed by the outbox worker.
- `v1`: route supported built-in events through the durable outbox. If the event cannot be persisted/routed, log the failure and fall back to the legacy sender rather than silently dropping the alert.

Use `shadow` to compare event/template/task matching and record counts before enabling `v1`; do not run both senders for the same built-in event. Suggested operator runbook (not executed by this change):

1. Keep `legacy` while capturing the current sender's payloads in a staging sink for every supported built-in event type.
2. In a staging Hub, set `shadow` before startup. Confirm each supported event produces shadow rows with zero outbox attempts and that the legacy sink receives exactly one payload per configured legacy destination. Compare event type, device, message, timestamp, severity and data; the automated parity fixtures cover payload structure.
3. Investigate every count or payload mismatch before switching. Check `notification_deliveries` status counts and legacy delivery-state/audit records; don't use shadow row count alone as proof that a provider accepted a message.
4. In an operator-approved window, set `v1` before startup and verify delivery success/failure and outbox backlog. If anything regresses, set `legacy` before the next startup; the legacy sender and tables remain intact.

Legacy compatibility tasks only route the existing built-in event family (`node_offline`, `node_recovery`, `resource_threshold`, `resource_recovery`, `scheduled_report`). They intentionally do not receive arbitrary events posted to `/api/v1/events`, including `development.completed` and `review.completed`; configure an explicit v1 task if those should be sent to a destination. Existing compatibility tasks are normalized to this allowlist on database migration and shown read-only in the v1 task editor; manage them through the legacy Webhook section.

Telegram Bot secrets are encrypted using AES-256-GCM and require `VPS_COMMANDER_NOTIFICATION_KEY`, a 32-byte key encoded as 64 hex characters or base64. The Hub's storage open path validates the variable during startup; a malformed configured value makes startup fail fast rather than silently disabling encryption. A missing key permits Hub startup and ordinary webhook operation, but saving Telegram/QQ credentials fails with `notification secret encryption key is not configured`. Before enabling Telegram, provide the key via the Hub service environment/drop-in and restart only during an approved window. Losing or changing the key makes existing encrypted credentials undecryptable; preserve the key securely and back it up separately from the database.

The default remains `legacy` until an operator explicitly selects another mode. This implementation does not change the production Hub environment or restart the service.

The repository agent guidance in `AGENTS.md` describes the development/review completion CLI. Emit only at final acceptance boundaries, never for intermediate commits. Missing runtime URL/token means skip and report; it must not block development. This is repository workflow guidance, not an automatically installed external agent hook.

## Non-goals for the first slice

- No release, deployment, push, merge or tag.
- No deletion of legacy tables.
- No claim that QQ proactive alerts work.
- No arbitrary template execution, automatic P1 escalation from untrusted clients, or direct caller-specified channel bypass.
