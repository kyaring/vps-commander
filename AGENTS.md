# VPS-Commander agent completion notifications

When working on VPS-Commander tasks, use the custom event API only at the final acceptance boundary, not for every intermediate commit or partial fix.

## Development completion

After the implementation task is complete, required tests/build checks pass, and the final commit SHA is known, run the non-blocking workflow helper. It skips successfully if runtime credentials are absent:

```sh
scripts/notify-agent-completion.sh development-completed \
  --task-id '<stable-task-id>' --commit '<final commit SHA>' \
  --summary '<concise result and tests>'
```

## Review completion

After an independent review has a final `PASS` or `FAIL` decision, run the helper using the reviewed commit SHA:

```sh
scripts/notify-agent-completion.sh review-completed \
  --task-id '<review-task-id>' --commit '<reviewed commit SHA>' \
  --result PASS --summary '<concise review result>'
```

Use `FAIL` for a final failed review, not for a transient test failure that is still being fixed. Keep summaries short and never include API tokens, passwords, private keys, webhook URLs, or full sensitive payloads. Do not request P1 or severity escalation unless the caller is explicitly authorized as admin.

If the event token or Hub URL is not configured, the helper prints `SKIP` and exits successfully; do not block development or claim that a notification was sent. The helper is an explicit final-boundary action, not an automatic hook on every commit.
