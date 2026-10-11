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
- Default policy: external callers cannot self-assign P1. Event types have server-owned default severities; any future severity override must require a separate explicit privileged scope and be audited.
- Require a bounded JSON body (32 KiB initially), validate event type against registered templates, allowlist variable names/types and cap string lengths. Treat all values as data; escape according to the output format.
- Apply a per-token token bucket and return `429` with `Retry-After`; audit accepted, rejected, rate-limited and idempotent-replay outcomes without logging credentials or full sensitive payloads.
- `Idempotency-Key` is optional; when present it is unique per token. A repeated key with the same request returns the original event result; reusing the key with a different request returns `409`.

## Compatibility migration

- Keep the old `webhook_targets` and `webhook_delivery_state` tables during the transition.
- The initial migration backfills each legacy webhook to a namespaced channel (`legacy-webhook:<id>`) and default task (`legacy-task:<id>`) using the raw-event JSON template. It also carries over last delivery status.
- Legacy create/update/delete and delivery-state writes update the shadow channel/task in the same SQLite transaction. Deletion disables shadow rows instead of removing them, preserving future delivery history.
- The current sender continues to read the old tables until the new router and provider adapters have passed tests. Do not cut over the sender in the schema-only step.

## Staged implementation

1. Additive schema, idempotent legacy backfill, transactional shadow writes, and storage tests. No behavior change.
2. Add channel/template/task management APIs and safe response DTOs; add `events:emit` to token scope validation and tests.
3. Implement normalized event acceptance, idempotency, per-token rate limit and audit; route to durable delivery rows.
4. Add provider adapters: Telegram proactive sends; QQ official bot is capability-limited/passive-only unless a supported proactive mode is confirmed. Add test-send/status reporting and credential non-echo tests.
5. Migrate built-in offline/resource/report notifications to templates/tasks while comparing output with the old sender; switch routing only after parity tests.
6. Add UI for channels, templates and tasks; add development-complete and review-complete emitters after the event API is stable.

## Non-goals for the first slice

- No release, deployment, push, merge or tag.
- No deletion of legacy tables.
- No claim that QQ proactive alerts work.
- No arbitrary template execution, automatic P1 escalation from untrusted clients, or direct caller-specified channel bypass.
